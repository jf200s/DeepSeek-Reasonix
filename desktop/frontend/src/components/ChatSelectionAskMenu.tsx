// The transcript's selection action. The menu exists only while a selection is
// open, so Transcript loads it lazily (the same way it loads the turn
// navigator) and keeps this out of its own bundle.
import { MessageSquarePlus } from "lucide-react";
import { FloatingMenu, FloatingMenuItems } from "./FloatingMenu";
import { useT } from "../lib/i18n";

export function ChatSelectionAskMenu({ x, y, onAsk }: { x: number; y: number; onAsk: () => void }) {
  const t = useT();
  return (
    <FloatingMenu x={x} y={y} estimatedHeight={44}>
      <FloatingMenuItems
        items={[{
          icon: <MessageSquarePlus size={14} />,
          label: t("sideChat.openFromSelection"),
          onSelect: onAsk,
        }]}
      />
    </FloatingMenu>
  );
}
