let fallbackWriteText: ((text: string) => Promise<void>) | null = null;

export async function writeClipboardText(text: string): Promise<void> {
  const nativeWrite = nativeClipboardWriteText();
  if (nativeWrite) {
    try {
      await nativeWrite(text);
      return;
    } catch {
      // Some browsers expose Clipboard API but reject it in local HTTP embeds.
      // Fall through to the textarea path for explicit user copy actions.
    }
  }
  await writeClipboardTextWithTextArea(text);
}

export function installClipboardWriteFallback() {
  if (typeof navigator === "undefined") return;
  if (nativeClipboardWriteText()) return;
  fallbackWriteText = (text: string) => writeClipboardTextWithTextArea(text);
  try {
    if (navigator.clipboard) {
      Object.defineProperty(navigator.clipboard, "writeText", {
        configurable: true,
        value: fallbackWriteText,
        writable: true,
      });
      return;
    }
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: fallbackWriteText },
      writable: true,
    });
  } catch (error) {
    console.error("clipboard fallback install failed", error);
  }
}

function nativeClipboardWriteText(): ((text: string) => Promise<void>) | null {
  if (typeof navigator === "undefined") return null;
  const clipboard = navigator.clipboard;
  const writeText = clipboard?.writeText;
  if (typeof writeText !== "function" || writeText === fallbackWriteText) {
    return null;
  }
  return writeText.bind(clipboard);
}

async function writeClipboardTextWithTextArea(text: string): Promise<void> {
  if (typeof document === "undefined" || !document.body) {
    throw new Error("Clipboard API not available");
  }
  const activeElement = document.activeElement;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "-9999px";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  try {
    const copied = document.execCommand("copy");
    if (!copied) throw new Error("Clipboard copy command failed");
  } finally {
    document.body.removeChild(textarea);
    if (
      activeElement &&
      "focus" in activeElement &&
      typeof activeElement.focus === "function"
    ) {
      activeElement.focus();
    }
  }
}
