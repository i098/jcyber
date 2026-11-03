import assert from "node:assert/strict";
import test from "node:test";

import { observeGraphTheme, readGraphTheme } from "./graph-theme.ts";

test("graph colors follow root theme changes and release their observer", (t) => {
  let dark = false;
  let notify;
  let disconnected = false;
  const root = { classList: { contains: () => dark }, dataset: { themePreset: "default" } };
  const context = {
    fillStyle: "",
    clearRect() {},
    fillRect() {},
    getImageData() {
      return { data: this.fillStyle === "dark" ? [20, 20, 20, 255] : [250, 250, 250, 255] };
    },
  };
  const globals = {
    document: {
      documentElement: root,
      createElement(tag) {
        assert.equal(tag, "canvas");
        return { getContext: () => context };
      },
    },
    getComputedStyle(element) {
      assert.equal(element, root);
      return {
        getPropertyValue(token) {
          const surface = ["--background", "--card", "--muted"].includes(token);
          return surface === dark ? "dark" : "light";
        },
      };
    },
    MutationObserver: class {
      constructor(callback) {
        notify = callback;
      }
      observe(element, options) {
        assert.equal(element, root);
        assert.deepEqual(options.attributeFilter, ["class", "data-theme-preset"]);
      }
      disconnect() {
        disconnected = true;
      }
    },
  };
  for (const [name, value] of Object.entries(globals)) {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, name);
    Object.defineProperty(globalThis, name, { value, configurable: true });
    t.after(() => {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    });
  }

  const light = readGraphTheme();
  assert.equal(light.background, "rgba(250, 250, 250, 1)");
  assert.equal(light.foreground, "rgba(20, 20, 20, 1)");
  const updates = [];
  const stop = observeGraphTheme(() => updates.push(readGraphTheme()));

  notify(); // Unrelated class mutations, such as disable-transitions, do not redraw.
  assert.equal(updates.length, 0);
  dark = true;
  notify();
  assert.equal(updates[0].background, light.foreground);
  assert.equal(updates[0].foreground, light.background);
  assert.equal(updates[0].muted, light.foreground);
  root.dataset.themePreset = "soft-pop";
  notify();
  assert.equal(updates.length, 2);
  dark = false;
  notify();
  assert.deepEqual(updates[2], light);
  stop();
  assert.equal(disconnected, true);
});
