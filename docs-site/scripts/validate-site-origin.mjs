import { getSiteOrigin } from "../lib/site-origin.ts";

try {
  getSiteOrigin({
    NODE_ENV: "production",
    NEXT_PUBLIC_SITE_URL: process.env.NEXT_PUBLIC_SITE_URL,
  });
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
