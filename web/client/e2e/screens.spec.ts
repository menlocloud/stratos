// Screenshot matrix: every route × {light, dark} against the mock app.
// Output: e2e/shots/<name>-<theme>.png — the gallery reviewed during the
// restyle. Not a pixel-diff suite; it asserts the page rendered (no auth
// wall, no crash) and captures the state of the world.
import { expect, test } from "@playwright/test"
import { routes, themes } from "./routes.ts"

for (const theme of themes) {
  for (const route of routes) {
    test(`shot ${route.name} [${theme}]`, async ({ page }) => {
      await page.emulateMedia({ reducedMotion: "reduce" })
      await page.addInitScript((t) => localStorage.setItem("stratos.theme", t), theme)
      await page.goto(route.path)
      await page.waitForLoadState("networkidle")
      await page.evaluate(() => document.fonts.ready)
      // settle: mock latency + suspense chunks
      await page.waitForTimeout(500)

      const text = await page.evaluate(() => document.body.innerText)
      expect(text.length, "page should render content").toBeGreaterThan(40)
      if (!route.public) {
        expect(text, "should not hit the auth wall").not.toMatch(/sign in to continue/i)
      }

      await page.screenshot({
        path: `e2e/shots/${route.name}-${theme}.png`,
        animations: "disabled",
        fullPage: false,
      })
    })
  }
}
