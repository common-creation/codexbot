import { useEffect, useId, useRef, useState } from "react";
import type { ChangeEvent } from "react";
import type { IconUpload } from "../types";
import { AgentAvatar } from "./AgentAvatar";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

const allowedTypes = new Set(["image/png", "image/jpeg", "image/gif"]);
const maximumBytes = 2 * 1024 * 1024;

type AgentIconPickerProps = {
  name: string;
  iconUrl?: string;
  value: IconUpload | null | undefined;
  onChange: (value: IconUpload | null | undefined) => void;
  disabled?: boolean;
  onBusyChange?: (busy: boolean) => void;
};

export function AgentIconPicker({ name, iconUrl, value, onChange, disabled, onBusyChange }: AgentIconPickerProps) {
  const inputId = useId();
  const input = useRef<HTMLInputElement>(null);
  const reader = useRef<FileReader | null>(null);
  const callbacks = useRef({ onChange, onBusyChange });
  callbacks.current = { onChange, onBusyChange };
  const [reading, setReading] = useState(false);
  const [error, setError] = useState<string>();
  const previewUrl = value === undefined ? iconUrl : value ? iconPreviewUrl(value.data) : undefined;

  const stopReading = () => {
    const current = reader.current;
    reader.current = null;
    if (!current) return;
    current.onload = null;
    current.onerror = null;
    current.onabort = null;
    current.abort();
  };

  const setBusy = (busy: boolean) => {
    setReading(busy);
    callbacks.current.onBusyChange?.(busy);
  };

  useEffect(() => () => {
    if (reader.current) {
      stopReading();
      callbacks.current.onBusyChange?.(false);
    }
  }, []);

  const chooseIcon = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (!file) return;
    stopReading();
    setError(undefined);
    if (!allowedTypes.has(file.type)) {
      setError("Choose a PNG, JPEG, or GIF image.");
      setBusy(false);
      return;
    }
    if (file.size > maximumBytes) {
      setError("Choose an image no larger than 2 MB.");
      setBusy(false);
      return;
    }
    const next = new FileReader();
    reader.current = next;
    setBusy(true);
    const fail = () => {
      if (reader.current !== next) return;
      reader.current = null;
      setError("Could not read the image. Please choose it again.");
      setBusy(false);
    };
    next.onload = () => {
      if (reader.current !== next) return;
      if (typeof next.result !== "string") {
        fail();
        return;
      }
      reader.current = null;
      callbacks.current.onChange({ data: next.result.slice(next.result.indexOf(",") + 1) });
      setBusy(false);
    };
    next.onerror = fail;
    next.onabort = fail;
    try {
      next.readAsDataURL(file);
    } catch {
      fail();
    }
  };

  const removeIcon = () => {
    stopReading();
    setBusy(false);
    setError(undefined);
    if (input.current) input.current.value = "";
    callbacks.current.onChange(null);
  };

  return (
    <div className="agent-icon-picker">
      <Label htmlFor={inputId}>Agent icon</Label>
      <div className="agent-icon-picker-content">
        <div role="img" aria-label="Agent icon preview"><AgentAvatar agent={{ id: "preview", name, iconUrl: previewUrl }} size="large" /></div>
        <div className="agent-icon-picker-controls">
          <Input ref={input} id={inputId} type="file" accept="image/png,image/jpeg,image/gif" disabled={disabled}
            aria-describedby={`${inputId}-hint${error ? ` ${inputId}-error` : ""}`} aria-invalid={!!error} onChange={chooseIcon} />
          <p id={`${inputId}-hint`} className="agent-icon-hint">PNG, JPEG, or GIF, up to 2 MB. Changes apply when you save.</p>
          {(previewUrl || reading) && <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={removeIcon}>Remove icon</Button>}
          {reading && <p className="agent-icon-hint" role="status">Reading image…</p>}
          {error && <p id={`${inputId}-error`} className="agent-icon-error" role="alert">{error}</p>}
        </div>
      </div>
    </div>
  );
}

function iconPreviewUrl(data: string) {
  const type = data.startsWith("/9j/") ? "image/jpeg" : data.startsWith("R0lGOD") ? "image/gif" : "image/png";
  return `data:${type};base64,${data}`;
}
