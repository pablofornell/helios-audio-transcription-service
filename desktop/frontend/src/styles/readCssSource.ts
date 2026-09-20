/**
 * Test-only helper: reads a CSS file's raw source text off disk.
 *
 * `vitest.config.ts` runs with `css: false` for test speed (this project had
 * no test runner at all before this pass — see that file's comment). That
 * setting stubs every `*.css` import to `{}`, including `?raw` ones, so
 * there is no way to get real source text through Vite's module graph in a
 * test. `node:fs`/`node:path` are loaded through a non-literal dynamic
 * import specifier — assigned to a variable first — so TypeScript treats
 * the result as `any` rather than needing `@types/node`, which is not a
 * dependency of this project.
 *
 * Takes a path relative to the package root (this project's `cwd` when
 * running `npm test`), not a `import.meta.url`-relative one: the `jsdom`
 * test environment's global `URL` resolves a relative second argument
 * against jsdom's fake `window.location` rather than the module's real
 * `file:` URL, so `new URL("./foo.css", import.meta.url)` silently produces
 * the wrong path in this project's test environment.
 */

// Same reasoning as the dynamic imports above: a minimal local ambient
// declaration for the one global this file needs, instead of pulling in
// @types/node for it.
declare const process: { cwd: () => string };

export async function readCssSource(packageRelativePath: string): Promise<string> {
  const fsModuleName = "node:fs";
  const pathModuleName = "node:path";
  const fs = (await import(fsModuleName)) as any;
  const path = (await import(pathModuleName)) as any;
  return fs.readFileSync(path.join(process.cwd(), packageRelativePath), "utf8") as string;
}
