import { describe, expect, it } from "vitest";
import { shouldSubmitComposerKey } from "./composer";

const key = (overrides: Partial<KeyboardEvent> = {}) => ({
  key: "Enter",
  shiftKey: false,
  isComposing: false,
  keyCode: 13,
  ...overrides,
}) as KeyboardEvent;

describe("shouldSubmitComposerKey", () => {
  it("submits an unmodified Enter outside IME composition", () => {
    expect(shouldSubmitComposerKey(key())).toBe(true);
  });

  it("does not submit IME confirmation Enter", () => {
    expect(shouldSubmitComposerKey(key({ isComposing: true }))).toBe(false);
    expect(shouldSubmitComposerKey(key({ keyCode: 229 }))).toBe(false);
    expect(shouldSubmitComposerKey(key(), true)).toBe(false);
  });

  it("keeps Shift+Enter available for a newline", () => {
    expect(shouldSubmitComposerKey(key({ shiftKey: true }))).toBe(false);
  });
});
