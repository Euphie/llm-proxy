const previewOrigin = "http://localhost:3000";

interface SiteOriginEnvironment {
  NODE_ENV?: string;
  NEXT_PUBLIC_SITE_URL?: string;
}

const productionOriginError =
  "NEXT_PUBLIC_SITE_URL must be configured as a bare HTTPS origin for production metadata.";

const buildEnvironment: SiteOriginEnvironment = {
  NODE_ENV: process.env.NODE_ENV,
  NEXT_PUBLIC_SITE_URL: process.env.NEXT_PUBLIC_SITE_URL,
};

export function getSiteOrigin(environment: SiteOriginEnvironment = buildEnvironment): string {
  const configured = environment.NEXT_PUBLIC_SITE_URL?.trim();
  const preview = environment.NODE_ENV === "development" || environment.NODE_ENV === "test";
  if (!configured) {
    if (preview) return previewOrigin;
    throw new Error(productionOriginError);
  }

  try {
    const url = new URL(configured);
    if (
      (preview
        ? url.protocol !== "http:" && url.protocol !== "https:"
        : url.protocol !== "https:") ||
      url.username ||
      url.password ||
      url.pathname !== "/" ||
      url.search ||
      url.hash
    ) {
      throw new Error(productionOriginError);
    }
    return url.origin;
  } catch {
    if (preview) return previewOrigin;
    throw new Error(productionOriginError);
  }
}
