import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import App from "./App";

describe("App", () => {
  it("renders the acceptance preview and its safety label", async () => {
    render(<App />);
    expect(await screen.findByText("Workspace continuity, verified.")).toBeInTheDocument();
    expect(screen.getByText("Acceptance Preview")).toBeInTheDocument();
    expect(screen.getByText(/No real workspace will be changed/)).toBeInTheDocument();
    expect(screen.getByText("Implement parser recovery")).toBeInTheDocument();
  });
});
