import { beforeAll, describe, expect, it } from "vitest";
import { readCssSource } from "../styles/readCssSource";

// Reviewer finding: `.row:focus-within { outline: ...; }` painted the ring
// on every mouse click, not just keyboard focus — `:focus-within` fires
// whenever ANY descendant has focus, regardless of how it got there, and
// the actual focus target here is a descendant button (Cancel, ⋯), not
// `.row` itself. base.css's shared rule is `:focus-visible` for exactly
// this reason ("a mouse click on a control ... must not leave a ring
// behind"); `.row` needs the "any descendant, but only if THAT descendant
// would show a visible ring" combination, which is `:has(:focus-visible)`.
//
// vitest.config.ts runs with `css: false` (no real cascade to render in
// jsdom for this project — see that file's comment; even a `?raw` import
// comes back as `{}`), so this asserts on the actual rule in the source
// rather than a computed style — see readCssSource.ts.
describe("Row.module.css — focus ring", () => {
  let rowCss: string;

  beforeAll(async () => {
    rowCss = await readCssSource("src/components/Row.module.css");
  });

  it("paints the outline ring via :has(:focus-visible), not :focus-within", () => {
    const outlineRule = rowCss
      .split(/\}/)
      .find((rule) => rule.includes("outline:") && rule.includes("--ring-color"));
    expect(outlineRule).toBeTruthy();

    const selector = outlineRule!.split("{")[0]!.trim();
    expect(selector).toContain(":has(");
    expect(selector).toContain(":focus-visible");
    expect(selector).not.toBe(".row:focus-within");
  });
});
