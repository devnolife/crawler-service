"""Item schema shared by all spiders.

Keep this stable: webapp downstream relies on the field names. Add new fields
to the bottom rather than renaming existing ones.
"""
from __future__ import annotations

import scrapy


class ResearchItem(scrapy.Item):
    # Provenance.
    source = scrapy.Field()          # "garuda", "zenodo", ...
    source_id = scrapy.Field()       # native id at source if any

    # Core bibliographic fields.
    title = scrapy.Field()
    authors = scrapy.Field()         # list[str]
    journal = scrapy.Field()         # publication / venue
    year = scrapy.Field()            # int

    # Link & content.
    url = scrapy.Field()
    abstract = scrapy.Field()
    keywords = scrapy.Field()        # list[str]

    # Open access / dataset hints.
    is_open_access = scrapy.Field()  # bool | None
    has_dataset = scrapy.Field()     # bool | None
    dataset_urls = scrapy.Field()    # list[str]

    # Stamped by pipeline.
    scraped_at = scrapy.Field()      # ISO-8601 UTC
