export type ComposerKey = Pick<KeyboardEvent, "key" | "shiftKey" | "isComposing" | "keyCode">;

export function shouldSubmitComposerKey(event: ComposerKey, compositionActive = false): boolean {
  return event.key === "Enter"
    && !event.shiftKey
    && !compositionActive
    && !event.isComposing
    && event.keyCode !== 229;
}


export const MAX_ATTACHMENTS = 8;
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;

export function readAttachment(file: File): Promise<{ name: string; data: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(`Could not read ${file.name}.`));
    reader.onabort = () => reject(new Error(`Reading ${file.name} was cancelled.`));
    reader.onload = () => {
      const result = String(reader.result);
      resolve({ name: file.name, data: result.slice(result.indexOf(",") + 1) });
    };
    reader.readAsDataURL(file);
  });
}
