"""Pipelines for item post-processing."""
from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from itemadapter import ItemAdapter


class TimestampPipeline:
    """Stamp every item with the UTC scrape time."""

    def process_item(self, item, spider):  # noqa: ARG002 - scrapy signature
        adapter = ItemAdapter(item)
        adapter["scraped_at"] = datetime.now(timezone.utc).isoformat()
        return item


class PostgresPipeline:
    """Upsert items into Postgres `papers` table.

    Activated when ``CRAWLER_DATABASE_URL`` is set (or default reachable).
    Failures per-item are logged but don't kill the crawl.
    """

    UPSERT_SQL = """
        INSERT INTO papers (
            source, source_id, title, authors, journal, year, url, abstract,
            keywords, is_open_access, has_dataset, dataset_urls, scraped_at
        ) VALUES (
            %(source)s, %(source_id)s, %(title)s, %(authors)s, %(journal)s,
            %(year)s, %(url)s, %(abstract)s, %(keywords)s, %(is_open_access)s,
            %(has_dataset)s, %(dataset_urls)s, NOW()
        )
        ON CONFLICT (source, source_id) DO UPDATE SET
            title          = EXCLUDED.title,
            authors        = EXCLUDED.authors,
            journal        = EXCLUDED.journal,
            year           = EXCLUDED.year,
            url            = EXCLUDED.url,
            abstract       = EXCLUDED.abstract,
            keywords       = EXCLUDED.keywords,
            is_open_access = EXCLUDED.is_open_access,
            has_dataset    = EXCLUDED.has_dataset,
            dataset_urls   = EXCLUDED.dataset_urls,
            scraped_at     = NOW();
    """

    def __init__(self) -> None:
        self.conn = None
        self.enabled = False
        self.inserted = 0
        self.failed = 0

    def open_spider(self, spider) -> None:  # noqa: D401
        try:
            import psycopg

            from . import db

            db.ensure_schema()
            self.conn = psycopg.connect(db.database_url())
            self.enabled = True
            spider.logger.info("PostgresPipeline connected to %s", db.database_url())
        except Exception as exc:  # noqa: BLE001
            spider.logger.warning(
                "PostgresPipeline disabled (could not connect: %s)", exc
            )
            self.enabled = False

    def close_spider(self, spider) -> None:  # noqa: D401
        if self.conn is not None:
            try:
                self.conn.close()
            except Exception:  # noqa: BLE001
                pass
        spider.logger.info(
            "PostgresPipeline summary: inserted/updated=%d failed=%d",
            self.inserted,
            self.failed,
        )

    def process_item(self, item, spider):  # noqa: D401
        if not self.enabled or self.conn is None:
            return item
        adapter = ItemAdapter(item)
        row: dict[str, Any] = {
            "source": adapter.get("source") or "",
            "source_id": str(adapter.get("source_id") or ""),
            "title": adapter.get("title") or "",
            "authors": adapter.get("authors") or [],
            "journal": adapter.get("journal"),
            "year": adapter.get("year"),
            "url": adapter.get("url") or "",
            "abstract": adapter.get("abstract"),
            "keywords": adapter.get("keywords") or [],
            "is_open_access": adapter.get("is_open_access"),
            "has_dataset": adapter.get("has_dataset"),
            "dataset_urls": adapter.get("dataset_urls") or [],
        }
        if not row["source"] or not row["source_id"] or not row["title"]:
            spider.logger.debug("skip incomplete item: %s", row.get("url"))
            return item
        try:
            with self.conn.cursor() as cur:
                cur.execute(self.UPSERT_SQL, row)
            self.conn.commit()
            self.inserted += 1
        except Exception as exc:  # noqa: BLE001
            self.failed += 1
            try:
                self.conn.rollback()
            except Exception:  # noqa: BLE001
                pass
            spider.logger.error("pg upsert failed: %s", exc)
        return item
