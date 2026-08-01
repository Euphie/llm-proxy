import { searchIndex, searchIndexVersion } from "@/lib/chapters";

const etag = `"${searchIndexVersion}"`;

export function GET(request: Request) {
  const requestedVersion = new URL(request.url).searchParams.get("v");
  if (requestedVersion && requestedVersion !== searchIndexVersion) {
    return Response.json(
      { error: "search_index_version_mismatch", currentVersion: searchIndexVersion },
      {
        status: 409,
        headers: {
          "Cache-Control": "no-store",
          "X-Content-Type-Options": "nosniff",
          "X-Search-Index-Version": searchIndexVersion,
        },
      },
    );
  }
  const cacheControl = requestedVersion
    ? "public, max-age=31536000, immutable"
    : "public, max-age=0, must-revalidate";
  const headers = {
    "Cache-Control": cacheControl,
    ETag: etag,
    "X-Content-Type-Options": "nosniff",
    "X-Search-Index-Version": searchIndexVersion,
  };
  if (request.headers.get("if-none-match") === etag) {
    return new Response(null, { status: 304, headers });
  }
  return Response.json(searchIndex, {
    headers,
  });
}
