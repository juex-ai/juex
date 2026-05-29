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
  let execCommandName = "";
  let textareaValue = "";
  const textarea = {
    value: "",
    style: {} as Record<string, string>,
    setAttribute() {},
    focus() {},
    select() {},
  };
  defineGlobal("navigator", {});
  defineGlobal("document", {
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
  });

  installClipboardWriteFallback();
  await navigator.clipboard.writeText("fallback copy");

  assert.equal(textareaValue, "fallback copy");
  assert.equal(execCommandName, "copy");
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
