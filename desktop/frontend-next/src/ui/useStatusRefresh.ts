import { useCallback, useEffect, useRef } from "react";
import type { AgentPort, SessionStatus } from "../port/port";

const FIRST_MS = 1000;
const CAP_MS = 15_000;

/** One way to ask the kernel for a pane's status that does not give up. A turn
 *  boundary is the moment the snapshot is asked for, and the snapshot is what
 *  closes or vouches for a run, so a read lost to the network leaves the pane
 *  believing what it last heard — for a pane nobody is looking at, nothing else
 *  asks again. A failed read is therefore asked again with growing gaps until it
 *  answers, and stops when the pane goes away. terminal names a failure no retry
 *  can repair, such as the runtime the pane names no longer existing. */
export function useStatusRefresh(port: AgentPort, adopt: (status: SessionStatus) => void, settled: () => void, terminal?: (e: unknown) => boolean) {
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const tries = useRef(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      clearTimeout(timer.current);
      tries.current = 0;
    };
  }, [port]);
  const refresh = useCallback((): Promise<void> => port.status().then(
    (status) => {
      clearTimeout(timer.current);
      tries.current = 0;
      adopt(status);
      settled();
    },
    (e: unknown) => {
      if (!alive.current) return;
      if (terminal?.(e)) {
        clearTimeout(timer.current);
        return;
      }
      clearTimeout(timer.current);
      const gap = Math.min(CAP_MS, FIRST_MS * 2 ** tries.current++);
      timer.current = setTimeout(() => void refresh(), gap);
    },
  ), [port, adopt, settled, terminal]);
  return refresh;
}
