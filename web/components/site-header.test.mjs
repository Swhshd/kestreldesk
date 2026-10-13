import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { describe, it } from "node:test";

const source = await readFile(new URL("./site-header.tsx", import.meta.url), "utf8");

describe("site header preferences", () => {
  it("renders palette and theme controls without a locale switcher", () => {
    assert.doesNotMatch(source, /LocaleSwitcher/);
    assert.match(source, /import \{ PaletteToggle \} from "@\/components\/palette-toggle"/);
    assert.match(source, /import \{ ThemeToggle \} from "@\/components\/theme-toggle"/);
    assert.match(source, /<PaletteToggle \/>[\s\S]*<ThemeToggle \/>/);
  });
});
