# Putting the site on Cloudflare Pages (free)

The site is the folder `site/`: plain files, no build step, no script from other places. Cloudflare Pages serves it for free.

The easy steps on the site are the steps that were run: TESTING.md Parts A to C passed on native Windows 10 with a local model and with a hosted one (D-110, D-111). Publishing does not need the GitHub repository: Direct Upload works without it. The site is the pages `index.html` and `docs.html` (the documentation, with a guide for newcomers), the screenshots in `img/`, and the files they use; upload the whole folder.

## Before each publish

1. Check that the "Pre-release" paragraph of the first section and the table of models say what is true by then (what has been run, where), and point step 2 at the release archives if there are any.
2. `go test ./internal/sitecheck`. It checks that nothing is loaded from elsewhere (the only link to another address is the one to the source on GitHub), that the headers are strict, that both pages work in a browser at desktop and phone width, and that `docs.html` describes every command, flag, configuration key, built-in rule, tool and exit code of the program, that its examples load, and that every screenshot exists, has its true size and a description.
3. If a flag or a command changes, the check fails until `docs.html` (and the guide in `docs/GETTING-STARTED.md`, which shows the same screenshots) says so. The screenshots are real: retake them when the viewer or the terminal output changes in a way a reader would notice.

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
