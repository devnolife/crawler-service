BOT_NAME = "revisi_crawler"

SPIDER_MODULES = ["revisi_crawler.spiders"]
NEWSPIDER_MODULE = "revisi_crawler.spiders"

# Identify ourselves politely.
USER_AGENT = "revisi-studio-crawler/0.1 (+https://revisi-studio.id)"

# Obey robots.txt.
ROBOTSTXT_OBEY = True

# Throttle so we don't hammer sources.
CONCURRENT_REQUESTS = 8
CONCURRENT_REQUESTS_PER_DOMAIN = 2
DOWNLOAD_DELAY = 1.5
AUTOTHROTTLE_ENABLED = True
AUTOTHROTTLE_START_DELAY = 1.0
AUTOTHROTTLE_MAX_DELAY = 10.0
AUTOTHROTTLE_TARGET_CONCURRENCY = 1.5

# Retry on transient failures.
RETRY_ENABLED = True
RETRY_TIMES = 2

# Cache responses during development to be kind to sources.
HTTPCACHE_ENABLED = False
HTTPCACHE_EXPIRATION_SECS = 86400
HTTPCACHE_DIR = "httpcache"
HTTPCACHE_IGNORE_HTTP_CODES = [500, 502, 503, 504, 408, 429]
HTTPCACHE_STORAGE = "scrapy.extensions.httpcache.FilesystemCacheStorage"

# Default output encoding.
FEED_EXPORT_ENCODING = "utf-8"

# Item pipelines.
ITEM_PIPELINES = {
    "revisi_crawler.pipelines.TimestampPipeline": 300,
    "revisi_crawler.pipelines.PostgresPipeline": 400,
}

# Modern asyncio reactor for better concurrency.
TWISTED_REACTOR = "twisted.internet.asyncioreactor.AsyncioSelectorReactor"

# Quieter logs by default.
LOG_LEVEL = "INFO"
