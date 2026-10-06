import { useEffect, useRef, useState } from "react";

import { LineChart } from "./LineChart";
import type { Event, PoolStats, Sample } from "./types";

// Keep five minutes of one-second samples.
const HISTORY = 300;

// The admin API address. Default is the page origin, where pgmux serves the dashboard.
const api = new URLSearchParams(location.search).get("api") ?? "";

