import { useMemo, useRef, useState } from "react";

export interface Series {
  key: string;
  label: string;
  color: string; // a CSS variable such as var(--series-1)
  values: number[];
}

interface Props {
  title: string;
  unit: string;
  times: number[];
  series: Series[];
  /** An optional dashed reference line, such as the pool maximum. */
  reference?: { label: string; value: number };
}

const W = 640;
const H = 220;
const PAD = { top: 12, right: 128, bottom: 24, left: 44 };

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const mag = 10 ** Math.floor(Math.log10(v));
  const n = v / mag;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * mag;
}

function fmt(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 0 : 1)}k`;
  if (v >= 10 || Number.isInteger(v)) return `${Math.round(v)}`;
  return v.toFixed(v < 1 ? 2 : 1);
}
const clock = (t: number) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

/** A line chart with one y-axis, a legend, direct end labels and a crosshair tooltip. */
export function LineChart({ title, unit, times, series, reference }: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);

  // Four equal steps of a round size, so the ticks are round numbers.
  const yMax = useMemo(() => {
    const all = series.flatMap((s) => s.values).concat(reference ? [reference.value] : []);
    return niceMax(Math.max(1, ...all) / 4) * 4;
  }, [series, reference]);

  const n = times.length;
  const iw = W - PAD.left - PAD.right;
  const ih = H - PAD.top - PAD.bottom;
  const x = (i: number) => PAD.left + (n <= 1 ? iw : (i / (n - 1)) * iw);
  const y = (v: number) => PAD.top + ih - (v / yMax) * ih;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * yMax);

  function onMove(e: React.PointerEvent<SVGSVGElement>) {
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect || n === 0) return;
    const px = ((e.clientX - rect.left) / rect.width) * W;
    const i = Math.round(((px - PAD.left) / iw) * (n - 1));
    setHover(Math.max(0, Math.min(n - 1, i)));
  }

  const last = n - 1;
  // Push end labels apart vertically, so labels of close values do not overlap.
  const labelY = new Map<string, number>();
  if (n > 0) {
    const order = [...series].sort((a, b) => y(a.values[last]) - y(b.values[last]));
    let prev = -Infinity;
    for (const s of order) {
      const pos = Math.max(y(s.values[last]), prev + 13);
      labelY.set(s.key, pos);
      prev = pos;
    }
  }
  return (
    <figure className="card chart">
      <figcaption>
        <h2>{title}</h2>
        <ul className="legend">
          {series.map((s) => (
            <li key={s.key}>
              <span className="key" style={{ background: s.color }} />
              {s.label}
            </li>
          ))}
          {reference && (
            <li>
              <span className="key dashed" />
              {reference.label}
            </li>
          )}
        </ul>
      </figcaption>
      <div className="plot">
        <svg
          ref={svgRef}
          viewBox={`0 0 ${W} ${H}`}
          role="img"
          aria-label={`${title}, ${unit}`}
          onPointerMove={onMove}
          onPointerLeave={() => setHover(null)}
        >
          {ticks.map((t) => (
            <g key={t}>
              <line className="grid" x1={PAD.left} x2={W - PAD.right} y1={y(t)} y2={y(t)} />
              <text className="tick" x={PAD.left - 6} y={y(t)} textAnchor="end" dominantBaseline="middle">
                {fmt(t)}
              </text>
            </g>
          ))}
          <line className="axis" x1={PAD.left} x2={W - PAD.right} y1={y(0)} y2={y(0)} />
          {n > 1 && (
            <>
              <text className="tick" x={PAD.left} y={H - 6}>
                {clock(times[0])}
              </text>
              <text className="tick" x={W - PAD.right} y={H - 6} textAnchor="end">
                {clock(times[last])}
              </text>
            </>
          )}
          {reference && (
            <line className="reference" x1={PAD.left} x2={W - PAD.right} y1={y(reference.value)} y2={y(reference.value)} />
          )}
          {series.map((s) => (
            <g key={s.key}>
              <polyline
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(" ")}
              />
              {n > 0 && (
                <text className="end-label" x={x(last) + 8} y={labelY.get(s.key)} dominantBaseline="middle">
                  {s.label} {fmt(s.values[last])}
                </text>
              )}
            </g>
          ))}
          {hover !== null && (
            <g>
              <line className="crosshair" x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + ih} />
              {series.map((s) => (
                <circle key={s.key} cx={x(hover)} cy={y(s.values[hover])} r={4} fill={s.color} className="dot" />
              ))}
            </g>
          )}
        </svg>
        {hover !== null && (
          <div className="tooltip" style={{ left: `${(x(hover) / W) * 100}%` }}>
            <div className="tt-time">{clock(times[hover])}</div>
            {series.map((s) => (
              <div key={s.key} className="tt-row">
                <span className="tt-key" style={{ background: s.color }} />
                <strong>{s.values[hover].toLocaleString(undefined, { maximumFractionDigits: 1 })}</strong>
                <span>{s.label}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </figure>
  );
}
