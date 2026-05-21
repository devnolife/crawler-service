"""Postgres helpers for crawler + API.

Single source of truth for connection string and schema.
"""
from __future__ import annotations

import os
from contextlib import contextmanager
from typing import Iterator

import psycopg
from psycopg.rows import dict_row


DEFAULT_URL = "postgresql://postgres:postgres@127.0.0.1:5432/revisi_crawler"


def database_url() -> str:
    return os.environ.get("CRAWLER_DATABASE_URL", DEFAULT_URL)


SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS papers (
    id              BIGSERIAL PRIMARY KEY,
    source          TEXT        NOT NULL,
    source_id       TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    authors         TEXT[]      NOT NULL DEFAULT '{}',
    journal         TEXT,
    year            INTEGER,
    url             TEXT        NOT NULL,
    abstract        TEXT,
    keywords        TEXT[]      NOT NULL DEFAULT '{}',
    is_open_access  BOOLEAN,
    has_dataset     BOOLEAN,
    dataset_urls    TEXT[]      NOT NULL DEFAULT '{}',
    scraped_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source, source_id)
);

CREATE INDEX IF NOT EXISTS papers_year_idx        ON papers (year DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS papers_source_idx      ON papers (source);
CREATE INDEX IF NOT EXISTS papers_scraped_at_idx  ON papers (scraped_at DESC);

-- Full-text search index (simple config; cocok untuk multi-bahasa basic).
CREATE INDEX IF NOT EXISTS papers_fts_idx ON papers
    USING GIN (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')));
"""


@contextmanager
def connect() -> Iterator[psycopg.Connection]:
    """Yield a psycopg connection with autocommit off."""
    conn = psycopg.connect(database_url(), row_factory=dict_row)
    try:
        yield conn
    finally:
        conn.close()


def ensure_schema() -> None:
    """Create database (if needed) and apply schema."""
    url = database_url()
    # Database creation has to happen via the maintenance DB.
    target_db = url.rsplit("/", 1)[-1]
    admin_url = url.rsplit("/", 1)[0] + "/postgres"
    with psycopg.connect(admin_url, autocommit=True) as conn:
        exists = conn.execute(
            "SELECT 1 FROM pg_database WHERE datname = %s", (target_db,)
        ).fetchone()
        if not exists:
            conn.execute(f'CREATE DATABASE "{target_db}"')
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(SCHEMA_SQL)
        conn.commit()
