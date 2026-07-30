import { previewDashboard } from "./mock";
import type { Dashboard } from "./types";

interface AppBridge {
  GetDashboard(): Promise<Dashboard>;
  Initialize(): Promise<Dashboard>;
  SelectWorkspace(): Promise<string>;
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: AppBridge;
      };
    };
  }
}

function bridge(): AppBridge | undefined {
  return window.go?.main?.App;
}

export async function getDashboard(): Promise<Dashboard> {
  return bridge()?.GetDashboard() ?? previewDashboard;
}

export async function initialize(): Promise<Dashboard> {
  return bridge()?.Initialize() ?? previewDashboard;
}

export async function selectWorkspace(): Promise<string> {
  return bridge()?.SelectWorkspace() ?? "/Users/demo/Projects/sessionmgr";
}
