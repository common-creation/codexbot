import { useState } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { IconUpload } from "../types";
import { AgentIconPicker } from "./AgentIconPicker";

class MockFileReader {
  static instances: MockFileReader[] = [];
  result: string | ArrayBuffer | null = null;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  abort = vi.fn();
  readAsDataURL = vi.fn();
  constructor() { MockFileReader.instances.push(this); }
  complete(result: string) { this.result = result; this.onload?.(); }
}

beforeEach(() => {
  MockFileReader.instances = [];
  vi.stubGlobal("FileReader", MockFileReader);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function ControlledPicker({ onChange = vi.fn(), onBusyChange = vi.fn() }: {
  onChange?: (value: IconUpload | null | undefined) => void;
  onBusyChange?: (busy: boolean) => void;
}) {
  const [value, setValue] = useState<IconUpload | null>();
  return <AgentIconPicker name="Alice" iconUrl="/api/agents/one/icon?v=1" value={value}
    onChange={(next) => { setValue(next); onChange(next); }} onBusyChange={onBusyChange} />;
}

function selectFile(type = "image/png", size?: number) {
  const file = new File(["image"], "icon.png", { type });
  if (size !== undefined) Object.defineProperty(file, "size", { value: size });
  fireEvent.change(screen.getByLabelText("Agent icon"), { target: { files: [file] } });
  return file;
}

describe("AgentIconPicker", () => {
  it("reads a selected icon into raw base64 and previews it without saving", () => {
    const onChange = vi.fn();
    const onBusyChange = vi.fn();
    const { container } = render(<ControlledPicker onChange={onChange} onBusyChange={onBusyChange} />);
    const file = selectFile("image/png", 2 * 1024 * 1024);
    expect(MockFileReader.instances[0].readAsDataURL).toHaveBeenCalledWith(file);
    expect(onBusyChange).toHaveBeenLastCalledWith(true);
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent("Reading image");

    act(() => MockFileReader.instances[0].complete("data:image/png;base64,iVBORw0KGgo="));
    expect(onChange).toHaveBeenCalledWith({ data: "iVBORw0KGgo=" });
    expect(onBusyChange).toHaveBeenLastCalledWith(false);
    expect(container.querySelector(".agent-avatar img")).toHaveAttribute("src", "data:image/png;base64,iVBORw0KGgo=");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it.each([
    ["image/jpeg", "/9j/2w=="],
    ["image/gif", "R0lGODlh"],
  ])("preserves %s in the preview", (type, data) => {
    const { container } = render(<ControlledPicker />);
    selectFile(type);
    act(() => MockFileReader.instances[0].complete(`data:${type};base64,${data}`));
    expect(container.querySelector(".agent-avatar img")).toHaveAttribute("src", `data:${type};base64,${data}`);
  });

  it.each([
    ["image/svg+xml", 100, "Choose a PNG, JPEG, or GIF image."],
    ["image/png", 2 * 1024 * 1024 + 1, "Choose an image no larger than 2 MB."],
  ])("rejects invalid type/size before reading (%s, %i)", (type, size, message) => {
    const onChange = vi.fn();
    const { container } = render(<ControlledPicker onChange={onChange} />);
    selectFile(type, size);
    expect(screen.getByRole("alert")).toHaveTextContent(message);
    expect(screen.getByLabelText("Agent icon")).toHaveAttribute("aria-invalid", "true");
    expect(MockFileReader.instances).toHaveLength(0);
    expect(onChange).not.toHaveBeenCalled();
    expect(container.querySelector(".agent-avatar img")).toHaveAttribute("src", "/api/agents/one/icon?v=1");
  });

  it("removes an icon without applying a stale pending read", () => {
    const onChange = vi.fn();
    const onBusyChange = vi.fn();
    const { container } = render(<ControlledPicker onChange={onChange} onBusyChange={onBusyChange} />);
    selectFile();
    const pending = MockFileReader.instances[0];
    const finishPending = pending.onload;
    fireEvent.click(screen.getByRole("button", { name: "Remove icon" }));
    expect(pending.abort).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenCalledExactlyOnceWith(null);
    expect(onBusyChange).toHaveBeenLastCalledWith(false);
    act(() => { pending.result = "data:image/png;base64,b2xk"; finishPending?.(); });
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(container.querySelector(".agent-avatar img")).not.toBeInTheDocument();
    expect(container.querySelector(".agent-avatar")).toHaveTextContent("A");
  });

  it("only accepts the latest file when reads finish out of order", () => {
    const onChange = vi.fn();
    render(<ControlledPicker onChange={onChange} />);
    selectFile();
    const first = MockFileReader.instances[0];
    const finishFirst = first.onload;
    selectFile("image/jpeg");
    expect(first.abort).toHaveBeenCalledOnce();
    act(() => MockFileReader.instances[1].complete("data:image/jpeg;base64,/9j/2w=="));
    act(() => { first.result = "data:image/png;base64,b2xk"; finishFirst?.(); });
    expect(onChange).toHaveBeenCalledExactlyOnceWith({ data: "/9j/2w==" });
  });

  it("cancels pending reads when the form is closed", () => {
    const onChange = vi.fn();
    const onBusyChange = vi.fn();
    const { unmount } = render(<ControlledPicker onChange={onChange} onBusyChange={onBusyChange} />);
    selectFile();
    const pending = MockFileReader.instances[0];
    const finishPending = pending.onload;
    unmount();
    expect(pending.abort).toHaveBeenCalledOnce();
    expect(onBusyChange).toHaveBeenLastCalledWith(false);
    act(() => { pending.result = "data:image/png;base64,b2xk"; finishPending?.(); });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("reports file reading failures while keeping the saved icon", () => {
    const onChange = vi.fn();
    const onBusyChange = vi.fn();
    const { container } = render(<ControlledPicker onChange={onChange} onBusyChange={onBusyChange} />);
    selectFile();
    act(() => MockFileReader.instances[0].onerror?.());
    expect(screen.getByRole("alert")).toHaveTextContent("Could not read the image.");
    expect(onChange).not.toHaveBeenCalled();
    expect(onBusyChange).toHaveBeenLastCalledWith(false);
    expect(container.querySelector(".agent-avatar img")).toHaveAttribute("src", "/api/agents/one/icon?v=1");
  });

  it("disables changes while the form is saving", () => {
    render(<AgentIconPicker name="Alice" iconUrl="/api/agents/one/icon?v=1" value={undefined} onChange={vi.fn()} disabled />);
    expect(screen.getByLabelText("Agent icon")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remove icon" })).toBeDisabled();
  });
});
