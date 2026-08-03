#!/usr/bin/env bash
# Re-crawl curated EPrints repositories. Idempotent: upsert ke Postgres.
set -euo pipefail

cd "$(dirname "$0")/.."
CRAWL="bin/crawler-crawl"

# Build binary bila belum ada.
if [[ ! -x "$CRAWL" ]]; then
  echo "==> build $CRAWL"
  go build -o "$CRAWL" ./cmd/crawl
fi

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
  "$CRAWL" \
    -base-url "$url" \
    -query "$q" \
    -max-pages "$pages" || echo "  (failed, lanjut target berikutnya)"
done

echo "==> done"
