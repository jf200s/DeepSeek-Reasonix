import type { PaneReport } from "./Pane";

// The report a pane has before it says anything, and the one App falls back to
// when the active pane disappears. Kept beside App rather than in Pane so the
// fallback stays a window concern.
export const NO_REPORT: PaneReport = {
  status: null,
  workspace: null,
  title: "",
  steer: 0,
  run: "idle",
  live: false,
  cost: "",
  contextPercent: null,
  context: null,
  mcp: [],
  wallet: "",
  view: "flow",
  rows: 0,
  surfaces: 0,
};
