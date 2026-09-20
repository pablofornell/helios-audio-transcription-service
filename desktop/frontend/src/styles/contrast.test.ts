import { beforeAll, describe, expect, it } from "vitest";
import { readCssSource } from "./readCssSource";

// vitest.config.ts sets `css: false` (styles are stubbed to nothing for
// speed, since this project had no test runner at all before this pass —
// see that file's comment), so there is no real browser cascade to render
// against in these tests, and even a `?raw` import comes back as `{}`
// (see readCssSource.ts). These read the actual stylesheet source text off
// disk instead, which is what these two defects live in.
//
// Reviewer finding: `base.css`'s `prefers-contrast: more` block targeted a
// bare `:root` (specificity 0,1,0), but `tokens.css`'s
// `:root[data-theme="dark"]` is (0,2,0) and wins regardless of source
// order — and the app stamps `data-theme="dark"` on first run, so high
// contrast mode was a no-op on the app's actual default theme. Second,
// subtler bug: even where it did apply, `--border-subtle: var(--border)`
// and `--border: var(--border-strong)` in the same rule both resolve
// against the WINNING (i.e. post-override) value of `--border` for that
// element — custom properties don't have a "previous value" the way a
// Sass variable would — so both tokens collapsed onto `--border-strong`
// instead of promoting one step each.

/** Selectors here are simple enough (`:root`, `[attr="x"]`, `:not(...)`,
 * repeated `:root`) that a full CSS specificity engine is unwarranted; this
 * covers exactly those shapes. */
function specificity(selector: string): { idsAndClasses: number } {
  let s = selector;
  let idsAndClasses = 0;
  // :not(X) does not count itself, but X does — recurse then strip it.
  s = s.replace(/:not\(([^)]*)\)/g, (_, inner) => {
    idsAndClasses += specificity(inner).idsAndClasses;
    return "";
  });
  idsAndClasses += (s.match(/#[-\w]+/g) ?? []).length; // ids
  idsAndClasses += (s.match(/\.[-\w]+/g) ?? []).length; // classes
  idsAndClasses += (s.match(/\[[^\]]*\]/g) ?? []).length; // attributes
  idsAndClasses += (s.match(/(?<!:):[-\w]+/g) ?? []).length; // pseudo-classes, incl. :root
  return { idsAndClasses };
}

/** Returns just the innermost selector immediately wrapping a declaration —
 * i.e. strips any enclosing `@media (...) {` too, not just the previous
 * rule's trailing `}`. */
function extractSelector(css: string, declarationContaining: string): string {
  const declStart = css.indexOf(declarationContaining);
  if (declStart === -1) throw new Error(`fixture declaration not found: ${declarationContaining}`);
  const openBrace = css.lastIndexOf("{", declStart);
  const closeBraceOfPreviousRule = css.lastIndexOf("}", openBrace);
  const between = css.slice(closeBraceOfPreviousRule + 1, openBrace).replace(/\/\*[\s\S]*?\*\//g, "");
  const segments = between.split("{");
  return segments[segments.length - 1]!.trim();
}

/** Everything base.css's `prefers-contrast` rule needs to outrank, in every
 * theming path tokens.css defines. */
const COMPETING_DARK_SELECTORS = [':root[data-theme="dark"]', ':root:not([data-theme="light"])'];

describe("prefers-contrast: more vs. the dark theme selectors", () => {
  let baseCss: string;
  let tokensCss: string;

  beforeAll(async () => {
    baseCss = await readCssSource("src/styles/base.css");
    tokensCss = await readCssSource("src/styles/tokens.css");
  });

  it("outranks every selector tokens.css uses to redeclare --border under dark, regardless of source order", () => {
    const contrastSelector = extractSelector(baseCss, "--border-subtle: var(--border");
    const contrastSpecificity = specificity(contrastSelector).idsAndClasses;

    for (const darkSelector of COMPETING_DARK_SELECTORS) {
      expect(tokensCss).toContain(darkSelector);
      const darkSpecificity = specificity(darkSelector).idsAndClasses;
      expect(contrastSpecificity).toBeGreaterThan(darkSpecificity);
    }
  });

  it("does not promote a hairline token by reading var() off a property redeclared in the very same rule", () => {
    const mediaStart = baseCss.indexOf("@media (prefers-contrast: more)");
    expect(mediaStart).toBeGreaterThanOrEqual(0);
    const ruleBodyStart = baseCss.indexOf("{", baseCss.indexOf("{", mediaStart) + 1) + 1;
    const ruleBodyEnd = baseCss.indexOf("}", ruleBodyStart);
    const body = baseCss.slice(ruleBodyStart, ruleBodyEnd);

    const declarations = new Map<string, string>();
    for (const line of body.split(";")) {
      const match = line.match(/(--[\w-]+)\s*:\s*(.+)/s);
      if (match) declarations.set(match[1]!, match[2]!.trim());
    }
    expect(declarations.size).toBeGreaterThan(0);

    for (const [prop, value] of declarations) {
      const ref = value.match(/^var\((--[\w-]+)\)$/)?.[1];
      if (!ref) continue;
      expect(
        declarations.has(ref),
        `${prop}: var(${ref}) resolves against the winning value of ${ref} on this element — since this ` +
          `same rule ALSO redeclares ${ref}, ${prop} collapses onto whatever ${ref} was just promoted to, ` +
          `instead of promoting one step of its own.`,
      ).toBe(false);
    }
  });
});
