#!/usr/bin/env bash
# Re-crawl curated EPrints repositories. Idempotent: pipeline upserts.
set -euo pipefail

cd "$(dirname "$0")/.."
SCRAPY=".venv/bin/scrapy"

# Tambahkan target baru di bawah ini. Format: <base_url>|<query>|<max_pages>
TARGETS=(
  # UIN Sunan Kalijaga (robots.txt: allow)
  "https://digilib.uin-suka.ac.id|pendidikan agama islam|3"
  "https://digilib.uin-suka.ac.id|teknik informatika|3"
  "https://digilib.uin-suka.ac.id|psikologi|2"
  "https://digilib.uin-suka.ac.id|ekonomi syariah|2"
  "https://digilib.uin-suka.ac.id|hukum islam|2"
  "https://digilib.uin-suka.ac.id|matematika|2"
  "https://digilib.uin-suka.ac.id|sosiologi|2"
  "https://digilib.uin-suka.ac.id|komunikasi|2"
  "https://digilib.uin-suka.ac.id|manajemen|2"
  "https://digilib.uin-suka.ac.id|sastra inggris|2"
  # ETD UGM (robots.txt: allow /, tapi pakai cgi path)
  "https://etd.repository.ugm.ac.id|machine learning|2"
  # UNY (sebagian endpoint blacklist IP, coba kalau lolos)
  # "https://eprints.uny.ac.id|pembelajaran|2"
  # repository.unair / eprints.ums / eprints.umm memblok /cgi di robots.txt — skip
)

for entry in "${TARGETS[@]}"; do
  IFS='|' read -r url q pages <<<"$entry"
  echo "==> $url  ($q, ${pages} pages)"
  "$SCRAPY" crawl eprints \
    -a base_url="$url" \
    -a query="$q" \
    -a max_pages="$pages" \
    -s LOG_LEVEL=WARNING \
    -s FEEDS='' || echo "  (failed, lanjut target berikutnya)"
done

echo "==> done"
