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

