package dev.pgmux;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;
import java.io.StringReader;
import java.io.StringWriter;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.Test;
import org.postgresql.PGConnection;
import org.postgresql.PGNotification;
import org.postgresql.copy.CopyManager;

/** Shared client scenarios for pgmux with pgjdbc and HikariCP. */
class CompatTest {
  static final String HOST = env("PGMUX_HOST", "localhost");
  static final String PORT = env("PGMUX_PORT", "6432");
  static final String MODE = env("PGMUX_MODE", "transaction");
  static final String DB = MODE.equals("session") ? "sessiondb" : "appdb";
  static final String URL = "jdbc:postgresql://" + HOST + ":" + PORT + "/" + DB + "?sslmode=disable";
  static final AtomicInteger SEQ = new AtomicInteger();

  static String env(String name, String def) {
    String v = System.getenv(name);
    return v == null || v.isEmpty() ? def : v;
  }

  static String table(String prefix) {
    return "java_" + prefix + "_" + ProcessHandle.current().pid() + "_" + SEQ.incrementAndGet();
  }

  static Connection connect(String extra) throws SQLException {
    return DriverManager.getConnection(URL + extra, "app", "app");
  }

  static void exec(Connection c, String sql) throws SQLException {
    try (Statement s = c.createStatement()) {
      s.execute(sql);
    }
  }

  static long queryLong(Connection c, String sql) throws SQLException {
    try (Statement s = c.createStatement();
        ResultSet rs = s.executeQuery(sql)) {
      rs.next();
      return rs.getLong(1);
    }
  }

  @Test
  void basicCrud() throws Exception {
    try (Connection c = connect("")) {
      String t = table("crud");
      exec(c, "CREATE TABLE " + t + " (id int PRIMARY KEY, name text)");
      try (PreparedStatement ps = c.prepareStatement("INSERT INTO " + t + " VALUES (?, ?)")) {
        for (int i = 1; i <= 2; i++) {
          ps.setInt(1, i);
          ps.setString(2, "n" + i);
          ps.executeUpdate();
        }
      }
      exec(c, "UPDATE " + t + " SET name = 'z' WHERE id = 2");
      exec(c, "DELETE FROM " + t + " WHERE id = 1");
      assertEquals(1, queryLong(c, "SELECT count(*) FROM " + t));
      exec(c, "DROP TABLE " + t);
    }
  }

  @Test
  void transactionCommitRollback() throws Exception {
    try (Connection c = connect("")) {
      String t = table("tx");
      exec(c, "CREATE TABLE " + t + " (v int)");
      c.setAutoCommit(false);
      exec(c, "INSERT INTO " + t + " VALUES (1)");
      c.commit();
      exec(c, "INSERT INTO " + t + " VALUES (2)");
      c.rollback();
      assertThrows(SQLException.class, () -> exec(c, "SELECT 1/0"));
      c.rollback();
      c.setAutoCommit(true);
      assertEquals(1, queryLong(c, "SELECT count(*) FROM " + t));
      exec(c, "DROP TABLE " + t);
    }
  }

  /** pgjdbc switches to a named server-side statement after prepareThreshold runs. */
  @Test
  void preparedStatements() throws Exception {
    for (String threshold : List.of("5", "1", "0")) {
      for (int round = 0; round < 3; round++) {
        try (Connection c = connect("&prepareThreshold=" + threshold);
            PreparedStatement ps = c.prepareStatement("SELECT ?::int + 1")) {
          for (int i = 0; i < 100; i++) {
            ps.setInt(1, i);
            try (ResultSet rs = ps.executeQuery()) {
              rs.next();
              assertEquals(i + 1, rs.getInt(1), "threshold " + threshold + " iter " + i);
            }
          }
        }
      }
    }
  }

  @Test
  void concurrencyWithHikari() throws Exception {
    String t = table("conc");
    try (Connection c = connect("")) {
      exec(c, "CREATE TABLE " + t + " (id int PRIMARY KEY, n int)");
      exec(c, "INSERT INTO " + t + " VALUES (1, 0)");
    }
    HikariConfig cfg = new HikariConfig();
    cfg.setJdbcUrl(URL);
    cfg.setUsername("app");
    cfg.setPassword("app");
    cfg.setMaximumPoolSize(200);
    cfg.setConnectionTimeout(120_000);
    ExecutorService pool = Executors.newFixedThreadPool(200);
    try (HikariDataSource ds = new HikariDataSource(cfg)) {
      List<Future<?>> futures = new ArrayList<>();
      for (int w = 0; w < 200; w++) {
        futures.add(
            pool.submit(
                () -> {
                  try (Connection c = ds.getConnection()) {
                    c.setAutoCommit(false);
                    for (int i = 0; i < 50; i++) {
                      exec(c, "UPDATE " + t + " SET n = n + 1 WHERE id = 1");
                      c.commit();
                    }
                  }
                  return null;
                }));
      }
      for (Future<?> f : futures) {
        f.get();
      }
      try (Connection c = ds.getConnection()) {
        assertEquals(10_000, queryLong(c, "SELECT n FROM " + t));
        exec(c, "DROP TABLE " + t);
      }
    } finally {
      pool.shutdown();
    }
  }

  @Test
  void cancelQuery() throws Exception {
    try (Connection c = connect("");
        Statement s = c.createStatement()) {
      Thread canceler =
          new Thread(
              () -> {
                try {
                  Thread.sleep(300);
                  s.cancel();
                } catch (Exception ignored) {
                }
              });
      canceler.start();
      long start = System.nanoTime();
      SQLException e = assertThrows(SQLException.class, () -> s.execute("SELECT pg_sleep(30)"));
      assertEquals("57014", e.getSQLState());
      assertTrue(System.nanoTime() - start < 5_000_000_000L);
      assertEquals(1, queryLong(c, "SELECT 1"));
    }
  }

  @Test
  void copyInOut() throws Exception {
    try (Connection c = connect("")) {
      String t = table("copy");
      exec(c, "CREATE TABLE " + t + " (id int, name text)");
      StringBuilder sb = new StringBuilder();
      for (int i = 0; i < 100_000; i++) {
        sb.append(i).append("\trow ").append(i).append('\n');
      }
      CopyManager cm = c.unwrap(PGConnection.class).getCopyAPI();
      assertEquals(100_000, cm.copyIn("COPY " + t + " FROM STDIN", new StringReader(sb.toString())));
      StringWriter out = new StringWriter();
      assertEquals(100_000, cm.copyOut("COPY " + t + " TO STDOUT", out));
      exec(c, "DROP TABLE " + t);
    }
  }

  @Test
  void largeResult() throws Exception {
    try (Connection c = connect("")) {
      // pgjdbc streams with a portal only inside a transaction and with a fetch size.
      c.setAutoCommit(false);
      try (Statement s = c.createStatement()) {
        s.setFetchSize(10_000);
        long n = 0;
        try (ResultSet rs = s.executeQuery("SELECT g FROM generate_series(1, 1000000) g")) {
          while (rs.next()) {
            n++;
          }
        }
        assertEquals(1_000_000, n);
      }
      c.commit();
    }
  }

  @Test
  void listenNotify() throws Exception {
    try (Connection c = connect("&preferQueryMode=simple")) {
      if (MODE.equals("transaction")) {
        SQLException e = assertThrows(SQLException.class, () -> exec(c, "LISTEN java_compat"));
        assertTrue(e.getMessage().contains("transaction pooling"), e.getMessage());
        return;
      }
      exec(c, "LISTEN java_compat");
      try (Connection o = connect("")) {
        exec(o, "NOTIFY java_compat, 'hi'");
      }
      PGNotification[] got = c.unwrap(PGConnection.class).getNotifications(5000);
      assertEquals("hi", got[0].getParameter());
    }
  }

  @Test
  void tls() throws Exception {
    String ca = env("PGMUX_CA", Path.of("../../deploy/certs/ca.crt").toAbsolutePath().toString());
    Properties p = new Properties();
    p.setProperty("user", "app");
    p.setProperty("password", "app");
    p.setProperty("sslmode", "verify-full");
    p.setProperty("sslrootcert", ca);
    String url = "jdbc:postgresql://localhost:" + PORT + "/" + DB;
    try (Connection c = DriverManager.getConnection(url, p)) {
      assertEquals(1, queryLong(c, "SELECT 1"));
    }
  }
}
