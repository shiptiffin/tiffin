"""The database: an async SQLAlchemy engine on psycopg 3.

Tiffin sets DATABASE_URL (through the box's connection pooler) and
DATABASE_POOL_MAX (how many connections one instance should hold).
Migrations use DIRECT_DATABASE_URL instead (see migrations/env.py).
"""

import os
import re
from collections.abc import AsyncIterator

from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine


def database_url(name: str = "DATABASE_URL") -> str:
    """The URL in env var `name`, for SQLAlchemy's async psycopg driver."""
    url = os.environ.get(name) or os.environ.get("DATABASE_URL") or "postgresql://localhost/notes"
    return re.sub(r"^postgres(?:ql)?(?:\+\w+)?://", "postgresql+psycopg://", url)


engine = create_async_engine(
    database_url(),
    pool_size=int(os.environ.get("DATABASE_POOL_MAX") or 5),
    max_overflow=0,
    pool_pre_ping=True,  # a connection the pooler closed is replaced, not handed out
    # No server-side prepared statements through the transaction pooler: the
    # same rule as the JavaScript starters, and nothing for PgBouncer to track.
    connect_args={"prepare_threshold": None},
)
Session = async_sessionmaker(engine, expire_on_commit=False)


async def session() -> AsyncIterator[AsyncSession]:
    """A request's session (a FastAPI dependency)."""
    async with Session() as s:
        yield s
