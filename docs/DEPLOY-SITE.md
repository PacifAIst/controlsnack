# Putting the site on Cloudflare Pages (free)

The site is the folder `site/`: plain files, no build step, no script from other places. Cloudflare Pages serves it for free.

**Do not publish before the user's real-model tests have passed (TESTING.md): the easy steps on the site must be the steps that worked.** Publishing does not need the GitHub repository: Direct Upload works without it.

## Before the first publish

1. In `site/index.html`, change the footer's "Source: GitHub, publishing soon" to the real link once the repository exists, and remove the "The repository is not public yet" notes of the steps.
2. Change the "Pre-release" paragraph of the first section to say what is true by then (what has been run, where), and point step 2 at the release archives if there are any.
3. `go test ./internal/sitecheck` (it checks that nothing is loaded from elsewhere, that the headers are strict, and that the page works in a browser).

## Publish, by dragging the folder

1. Cloudflare dashboard → **Workers & Pages** → **Create** → **Pages** → **Upload assets**.
2. Name the project `controlsnack`, upload the `site` folder, **Deploy**. The site is then at `https://controlsnack.pages.dev`.

or from a terminal, with Node:

```
npx wrangler pages deploy site --project-name controlsnack
```

## The domain `www.controlsnack.com`

1. The domain must be on Cloudflare (its name servers set to Cloudflare's), or you add a `CNAME` for `www` to `controlsnack.pages.dev` where its DNS is.
2. Pages project → **Custom domains** → **Set up a custom domain** → `www.controlsnack.com`.
3. For the bare `controlsnack.com`: add it as a custom domain too (Cloudflare DNS flattens the apex), and in **Rules → Redirect Rules** send `controlsnack.com/*` to `https://www.controlsnack.com/$1` (301).
4. SSL/TLS mode **Full (strict)**; turn on **Always use HTTPS**.

## When the repository exists (later)

Pages → **Connect to Git** → pick `PacifAIst/controlsnack`, then: **Framework preset** None, **Build command** empty, **Build output directory** `site`. Every push to `main` then publishes. (`_headers` and `404.html` are read by Pages as they are.)

## What the site does and does not do

- It sets no cookies, loads no font, script, image or style from another address, and has no analytics. The content security policy in `site/_headers` forbids it.
- The test section keeps what is typed in the visitor's browser (`localStorage`) and builds a report there. The page never sends it: the visitor copies it or downloads it. A form that posts to a server would need a back end and a privacy notice, and is not part of it (D-106).
