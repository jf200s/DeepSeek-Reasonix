// Fixture for the companion-session dock path, driven by bench/side-chat-dock.mjs.
// It renders the real WorkspaceDockRegion (so the + menu, the dock tab strip and
// the companion panel are the production components) over a stubbed host that
// records the side-chat commands.
import React from "react";
import { createRoot } from "react-dom/client";
import { WorkspaceDockRegion } from "../src/app-shell/WorkspaceDockRegion";
import { LocaleProvider } from "../src/lib/i18n";
import { useActivityBarStore } from "../src/store/activityBar";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

// The dock's own strings come from the real translator, so the assertions read
// whatever locale the host resolves instead of pinning one here.

const sideChatCalls: string[] = [];
installDesktopHostStub({
  // The dock's tab picker lists host commands, and the companion composer asks
  // for the model list; both stay empty so the fixture exercises the dock itself.
  Commands: async () => [],
  Models: async () => [],
  ModelsForTab: async () => [],
  OpenSideChatForTab: async (parentTabId: string) => {
    sideChatCalls.push(`open:${parentTabId}`);
    return { TabID: "child-1", SessionID: "session-1", Ordinal: 1 };
  },
  CloseSideChatTab: async (tabID: string) => {
    sideChatCalls.push(`close:${tabID}`);
  },
});

useActivityBarStore.setState({ workspaceRoot: "/fixture", tabs: [], activeTabId: null });

Object.assign(window, { __sideChatCalls: sideChatCalls, __activityBarStore: useActivityBarStore });

function Fixture() {
  return (
    <LocaleProvider>
      <WorkspaceDockRegion
        visible
        overlay={false}
        mode="files"
        showContext={false}
        t={(key: string) => key}
        onPickEntry={() => {}}
        remote={{ onClose: () => {} }}
        context={{} as never}
        workspace={{ open: true, tabId: "parent-1", cwd: "/fixture", maximized: false, onClose: () => {}, onToggleMaximized: () => {} }}
        workspaceRoot="/fixture"
        workspaceKey="fixture"
      />
    </LocaleProvider>
  );
}

createRoot(document.getElementById("root")!).render(<Fixture />);
