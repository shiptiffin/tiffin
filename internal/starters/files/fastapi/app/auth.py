"""Who is signed in, from the box's sign-in service over plain HTTP.

With `services.auth` in tiffin.config.ts, the box runs sign-in for the app at
/api/auth on its own hosts and sets TIFFIN_AUTH_INTERNAL_URL. Asking
`$TIFFIN_AUTH_INTERNAL_URL/tiffin/session` with the request's cookie (or API
key) and the app's host answers the session, or 401 when signed out.
"""

import os
from typing import Any

from fastapi import HTTPException, Request

# The request headers that carry who is asking.
FORWARD = ("cookie", "x-api-key", "authorization", "user-agent", "x-forwarded-for", "x-forwarded-proto")


def enabled() -> bool:
    return bool(os.environ.get("TIFFIN_AUTH_INTERNAL_URL"))


async def session(request: Request) -> dict[str, Any] | None:
    """The signed-in session ({user, organization}), or None when signed out."""
    if not enabled():
        raise HTTPException(404, "Sign-in is off for this project: add services.auth to tiffin.config.ts.")
    headers = {h: v for h in FORWARD if (v := request.headers.get(h))}
    headers["x-tiffin-host"] = (
        request.headers.get("x-forwarded-host") or request.headers.get("host") or os.environ.get("TIFFIN_AUTH_HOST", "")
    )
    res = await request.app.state.http.get(os.environ["TIFFIN_AUTH_INTERNAL_URL"] + "/tiffin/session", headers=headers)
    if res.status_code in (401, 403):
        return None
    res.raise_for_status()
    return res.json()
