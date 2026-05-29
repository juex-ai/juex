import assert from "node:assert/strict";
import test from "node:test";
import {
  installClipboardWriteFallback,
  writeClipboardText,
} from "../../frontend/src/lib/clipboard.ts";

const navigatorDescriptor = Object.getOwnPropertyDescriptor(
  globalThis,
  "navigator",
);
const documentDescriptor = Object.getOwnPropertyDescriptor(
  globalThis,
  "document",
);

test.afterEach(() => {
  restoreGlobal("navigator", navigatorDescriptor);
  restoreGlobal("document", documentDescriptor);
});

test("writeClipboardText uses the native Clipboard API when available", async () => {
  let copied = "";
  defineGlobal("navigator", {
    clipboard: {
      writeText: async (text: string) => {
        copied = text;
      },
    },
  });

  await writeClipboardText("native copy");

  assert.equal(copied, "native copy");
});

test("installClipboardWriteFallback provides writeText when Clipboard API is unavailable", async () => {
  const copyDoc = createCopyDocument();
  defineGlobal("navigator", {});
  defineGlobal("document", copyDoc.document);

  installClipboardWriteFallback();
  await navigator.clipboard.writeText("fallback copy");

  assert.equal(copyDoc.textareaValue(), "fallback copy");
  assert.equal(copyDoc.execCommandName(), "copy");
  assert.equal(copyDoc.restoredFocus(), true);
});

test("installClipboardWriteFallback preserves existing clipboard properties", async () => {
  const copyDoc = createCopyDocument();
  defineGlobal("navigator", {
    clipboard: {
      readText: async () => "existing read",
    },
  });
  defineGlobal("document", copyDoc.document);

  installClipboardWriteFallback();
  await navigator.clipboard.writeText("preserved clipboard");

  assert.equal(await navigator.clipboard.readText(), "existing read");
  assert.equal(copyDoc.textareaValue(), "preserved clipboard");
});

test("writeClipboardText falls back when native Clipboard API rejects", async () => {
  const copyDoc = createCopyDocument();
  defineGlobal("navigator", {
    clipboard: {
      writeText: async () => {
        throw new Error("denied");
      },
    },
  });
  defineGlobal("document", copyDoc.document);

  await writeClipboardText("fallback after rejection");

  assert.equal(copyDoc.textareaValue(), "fallback after rejection");
  assert.equal(copyDoc.execCommandName(), "copy");
});

function defineGlobal(name: "navigator" | "document", value: unknown) {
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value,
  });
}

function restoreGlobal(
  name: "navigator" | "document",
  descriptor: PropertyDescriptor | undefined,
) {
  if (descriptor) {
    Object.defineProperty(globalThis, name, descriptor);
    return;
  }
  delete (globalThis as Record<string, unknown>)[name];
}

function createCopyDocument() {
  let execCommandName = "";
  let textareaValue = "";
  let restoredFocus = false;
  const activeElement = {
    focus() {
      restoredFocus = true;
    },
  };
  const textarea = {
    value: "",
    style: {} as Record<string, string>,
    setAttribute() {},
    focus() {},
    select() {},
  };
  return {
    document: {
      activeElement,
      body: {
        appendChild(node: typeof textarea) {
          textareaValue = node.value;
        },
        removeChild() {},
      },
      createElement(tagName: string) {
        assert.equal(tagName, "textarea");
        return textarea;
      },
      execCommand(command: string) {
        execCommandName = command;
        return true;
      },
    },
    execCommandName: () => execCommandName,
    restoredFocus: () => restoredFocus,
    textareaValue: () => textareaValue,
  };
}
