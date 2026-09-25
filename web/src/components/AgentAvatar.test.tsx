import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AgentAvatar } from "./AgentAvatar";

vi.mock("../api", () => ({ resolveApiUrl: (path: string) => `https://api.example.test${path}` }));

afterEach(cleanup);

describe("AgentAvatar", () => {
  it("shows an initial when no icon is set", () => {
    const { container } = render(<AgentAvatar agent={{ id: "one", name: "Alice" }} size="small" />);
    expect(container.querySelector(".agent-avatar")).toHaveClass("small");
    expect(container.querySelector(".agent-avatar")).toHaveTextContent("A");
    expect(container.querySelector("img")).not.toBeInTheDocument();
  });

  it("resolves saved icon URLs, falls back on errors, and retries a replacement", () => {
    const agent = { id: "one", name: "Alice", iconUrl: "/api/agents/one/icon?v=1" };
    const { container, rerender } = render(<AgentAvatar agent={agent} />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://api.example.test/api/agents/one/icon?v=1");
    expect(container.querySelector("img")).toHaveAttribute("alt", "");
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".agent-avatar")).toHaveTextContent("A");

    rerender(<AgentAvatar agent={{ ...agent, iconUrl: "/api/agents/one/icon?v=2" }} />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://api.example.test/api/agents/one/icon?v=2");
    rerender(<AgentAvatar agent={agent} />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://api.example.test/api/agents/one/icon?v=1");
  });

  it("uses local previews directly and falls back after removal", () => {
    const agent = { id: "one", name: "Alice", iconUrl: "data:image/png;base64,aWNvbg==" };
    const { container, rerender } = render(<AgentAvatar agent={agent} size="large" />);
    expect(container.querySelector("img")).toHaveAttribute("src", agent.iconUrl);
    rerender(<AgentAvatar agent={{ ...agent, iconUrl: undefined }} size="large" />);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".agent-avatar")).toHaveTextContent("A");
  });
});
