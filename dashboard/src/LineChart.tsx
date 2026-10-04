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

