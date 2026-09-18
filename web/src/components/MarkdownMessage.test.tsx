import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MarkdownMessage } from "./MarkdownMessage";

afterEach(cleanup);

describe("MarkdownMessage", () => {
  it("renders GFM task lists, strikethrough, links, and fenced code", () => {
    const { container } = render(<MarkdownMessage text={[
      "## 作業状況",
      "",
      "- [x] ~~旧実装~~",
      "- [ ] `react-markdown` を導入",
      "",
      "https://example.com",
      "",
      "```ts",
      "const message = '<strong>そのままのコード</strong>';",
      "console.log(message);",
      "```",
    ].join("\n")} />);

    expect(screen.getByRole("heading", { level: 2, name: "作業状況" })).toBeInTheDocument();
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes[0]).toBeChecked();
    expect(checkboxes[1]).not.toBeChecked();
    expect(checkboxes.every((checkbox) => checkbox.hasAttribute("disabled"))).toBe(true);
    expect(screen.getByText("旧実装").tagName).toBe("DEL");
    expect(screen.getByText("react-markdown").tagName).toBe("CODE");
    expect(screen.getByRole("link", { name: "https://example.com" })).toHaveAttribute("href", "https://example.com");
    expect(container.querySelector("pre code")?.textContent).toBe("const message = '<strong>そのままのコード</strong>';\nconsole.log(message);\n");
    expect(container.querySelector("pre strong")).not.toBeInTheDocument();
  });

  it("keeps raw HTML inert and removes unsafe link protocols", () => {
    const { container } = render(<MarkdownMessage text={[
      '<script>alert("unsafe")</script>',
      "",
      '<img src="x" onerror="alert(1)">',
      "",
      '[Unsafe](javascript:alert%281%29)',
      "",
      '[Documentation](https://example.com/docs)',
    ].join("\n")} />);

    expect(container.querySelector("script, img, [onerror]")).not.toBeInTheDocument();
    expect(container).toHaveTextContent('<script>alert("unsafe")</script>');
    expect(container).toHaveTextContent('<img src="x" onerror="alert(1)">');
    expect(screen.getByText("Unsafe").closest("a")).toHaveAttribute("href", "");
    const safeLink = screen.getByRole("link", { name: "Documentation" });
    expect(safeLink).toHaveAttribute("href", "https://example.com/docs");
    expect(safeLink).toHaveAttribute("target", "_blank");
    expect(safeLink).toHaveAttribute("rel", "noopener noreferrer");
  });
});
