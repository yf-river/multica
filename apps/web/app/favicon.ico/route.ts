export function GET() {
  return new Response("Redirecting to favicon.svg", {
    status: 308,
    headers: { Location: "/favicon.svg" },
  });
}
