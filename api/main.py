"""FastAPI service that exposes crawled `papers` over HTTP.

Run:
    uvicorn api.main:app --host 127.0.0.1 --port 8770
"""
from __future__ import annotations

import json
import logging
import os
from typing import Optional

import httpx
from fastapi import FastAPI, HTTPException, Query
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel

from revisi_crawler import db as crawler_db


logger = logging.getLogger("crawler.api")

OLLAMA_URL = os.environ.get("OLLAMA_URL", "http://127.0.0.1:11434")
OLLAMA_MODEL = os.environ.get("OLLAMA_MODEL", "qwen2.5:7b-instruct")


app = FastAPI(
    title="Revisi Studio Crawler API",
    version="0.1.0",
    description="Search crawled academic repositories.",
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["GET"],
    allow_headers=["*"],
)


class Paper(BaseModel):
    id: int
    source: str
    source_id: str
    title: str
    authors: list[str]
    journal: Optional[str] = None
    year: Optional[int] = None
    url: str
    abstract: Optional[str] = None
    keywords: list[str]
    is_open_access: Optional[bool] = None
    has_dataset: Optional[bool] = None
    dataset_urls: list[str]
    scraped_at: str


class SearchResponse(BaseModel):
    query: str
    total: int
    items: list[Paper]


@app.get("/health")
def health() -> dict:
    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute("SELECT COUNT(*) AS c FROM papers")
                row = cur.fetchone()
        return {"ok": True, "papers": int(row["c"])}
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(status_code=503, detail=f"db unavailable: {exc}")


@app.get("/api/v1/datasets/search", response_model=SearchResponse)
def search(
    q: str = Query("", description="Full-text query (title + abstract)"),
    source: Optional[str] = Query(None, description="Filter by source, e.g. 'eprints:digilib.uin-suka.ac.id'"),
    year_min: Optional[int] = Query(None, ge=1900, le=2100),
    year_max: Optional[int] = Query(None, ge=1900, le=2100),
    has_dataset: Optional[bool] = None,
    limit: int = Query(20, ge=1, le=100),
    offset: int = Query(0, ge=0),
) -> SearchResponse:
    where: list[str] = []
    params: dict = {}

    if q.strip():
        where.append(
            "to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')) "
            "@@ plainto_tsquery('simple', %(q)s)"
        )
        params["q"] = q
    if source:
        where.append("source = %(source)s")
        params["source"] = source
    if year_min is not None:
        where.append("year >= %(year_min)s")
        params["year_min"] = year_min
    if year_max is not None:
        where.append("year <= %(year_max)s")
        params["year_max"] = year_max
    if has_dataset is not None:
        where.append("has_dataset = %(has_dataset)s")
        params["has_dataset"] = has_dataset

    where_sql = ("WHERE " + " AND ".join(where)) if where else ""
    list_sql = f"""
        SELECT id, source, source_id, title, authors, journal, year, url,
               abstract, keywords, is_open_access, has_dataset, dataset_urls,
               to_char(scraped_at, 'YYYY-MM-DD"T"HH24:MI:SSOF') AS scraped_at
          FROM papers
          {where_sql}
          ORDER BY year DESC NULLS LAST, scraped_at DESC
          LIMIT %(limit)s OFFSET %(offset)s
    """
    count_sql = f"SELECT COUNT(*) AS c FROM papers {where_sql}"
    params.update({"limit": limit, "offset": offset})

    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute(count_sql, params)
                total = int(cur.fetchone()["c"])
                cur.execute(list_sql, params)
                rows = cur.fetchall()
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(status_code=503, detail=f"db error: {exc}")

    items = [Paper(**row) for row in rows]
    return SearchResponse(query=q, total=total, items=items)


class TrendPoint(BaseModel):
    year: int
    count: int


class TrendResponse(BaseModel):
    query: str
    total: int
    series: list[TrendPoint]


@app.get("/api/v1/datasets/trend", response_model=TrendResponse)
def trend(
    q: str = Query("", description="Topik untuk dianalisis trennya"),
    year_min: int = Query(2015, ge=1900, le=2100),
    year_max: int = Query(2030, ge=1900, le=2100),
) -> TrendResponse:
    """Aggregate jumlah paper per tahun untuk topik tertentu."""
    where = ["year IS NOT NULL", "year BETWEEN %(ymin)s AND %(ymax)s"]
    params: dict = {"ymin": year_min, "ymax": year_max}
    if q.strip():
        where.append(
            "to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')) "
            "@@ plainto_tsquery('simple', %(q)s)"
        )
        params["q"] = q
    sql = f"""
        SELECT year, COUNT(*)::int AS c
          FROM papers
         WHERE {' AND '.join(where)}
         GROUP BY year
         ORDER BY year
    """
    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, params)
                rows = cur.fetchall()
    except Exception as exc:  # noqa: BLE001
        logger.warning("trend query failed: %s", exc)
        raise HTTPException(status_code=503, detail=f"db error: {exc}")
    series = [TrendPoint(year=int(r["year"]), count=int(r["c"])) for r in rows]
    return TrendResponse(query=q, total=sum(p.count for p in series), series=series)


class CitationSuggestion(BaseModel):
    paper: Paper
    bibtex: str
    apa: str


class CitationResponse(BaseModel):
    paragraph_snippet: str
    keywords_used: list[str]
    suggestions: list[CitationSuggestion]


def _extract_keywords(text: str, k: int = 6) -> list[str]:
    """Naive keyword extractor: token >= 5 chars, freq-ranked, stopwords removed."""
    import re
    stop = {
        "yang", "dan", "atau", "untuk", "dengan", "dalam", "pada", "adalah", "tidak",
        "ini", "itu", "akan", "sebagai", "dari", "oleh", "tersebut", "secara", "telah",
        "sangat", "lebih", "namun", "tetapi", "agar", "karena", "sehingga", "their",
        "their", "which", "while", "where", "these", "those", "have", "been", "will",
        "this", "that", "with", "from", "into", "about", "than", "such", "also",
        "study", "research", "penelitian", "menggunakan", "metode", "hasil",
    }
    words = re.findall(r"[A-Za-zÀ-ÿ]{5,}", text.lower())
    freq: dict[str, int] = {}
    for w in words:
        if w in stop:
            continue
        freq[w] = freq.get(w, 0) + 1
    return [w for w, _ in sorted(freq.items(), key=lambda x: -x[1])[:k]]


def _format_bibtex(paper: dict) -> str:
    key = (paper["source"].split(":")[-1].split(".")[0] + str(paper["source_id"])).lower()
    author = " and ".join(paper["authors"]) if paper["authors"] else ""
    parts = [f"@misc{{{key},"]
    parts.append(f"  title  = {{{paper['title']}}},")
    if author:
        parts.append(f"  author = {{{author}}},")
    if paper.get("year"):
        parts.append(f"  year   = {{{paper['year']}}},")
    parts.append(f"  url    = {{{paper['url']}}},")
    parts.append(f"  note   = {{{paper['source']}}},")
    parts.append("}")
    return "\n".join(parts)


def _format_apa(paper: dict) -> str:
    authors = paper["authors"] or ["Anonim"]
    if len(authors) > 3:
        author_str = f"{authors[0]} dkk."
    else:
        author_str = ", ".join(authors)
    year = paper.get("year") or "t.t."
    return f"{author_str} ({year}). {paper['title']}. {paper['source']}. {paper['url']}"


@app.post("/api/v1/citations/suggest", response_model=CitationResponse)
def suggest_citations(payload: dict) -> CitationResponse:
    """Saran sitasi dari paragraf user. Ambil top-K paper relevan + format BibTeX/APA."""
    paragraph = (payload.get("paragraph") or "").strip()
    limit = int(payload.get("limit") or 5)
    if not paragraph or len(paragraph) < 20:
        raise HTTPException(status_code=400, detail="paragraph minimal 20 karakter")
    if limit < 1 or limit > 20:
        limit = 5

    keywords = _extract_keywords(paragraph)
    if not keywords:
        return CitationResponse(paragraph_snippet=paragraph[:160], keywords_used=[], suggestions=[])
    # OR-join keyword agar match lebih lenient (≥1 kata cocok cukup).
    # to_tsquery butuh format 'word1 | word2 | word3'. Escape karakter aneh.
    import re as _re
    safe = [_re.sub(r"[^A-Za-z0-9À-ÿ]", "", k) for k in keywords if len(k) >= 4]
    safe = [k for k in safe if k]
    if not safe:
        return CitationResponse(paragraph_snippet=paragraph[:160], keywords_used=keywords, suggestions=[])
    ts_query = " | ".join(safe)

    sql = """
        SELECT id, source, source_id, title, authors, journal, year, url,
               abstract, keywords, is_open_access, has_dataset, dataset_urls,
               to_char(scraped_at, 'YYYY-MM-DD"T"HH24:MI:SSOF') AS scraped_at,
               ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                       to_tsquery('simple', %(q)s)) AS rank
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', %(q)s)
         ORDER BY rank DESC, year DESC NULLS LAST
         LIMIT %(limit)s
    """
    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, {"q": ts_query, "limit": limit})
                rows = cur.fetchall()
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(status_code=503, detail=f"db error: {exc}")

    suggestions = []
    for r in rows:
        paper_dict = {k: r[k] for k in Paper.model_fields if k in r}
        suggestions.append(
            CitationSuggestion(
                paper=Paper(**paper_dict),
                bibtex=_format_bibtex(paper_dict),
                apa=_format_apa(paper_dict),
            )
        )
    return CitationResponse(
        paragraph_snippet=paragraph[:160],
        keywords_used=keywords,
        suggestions=suggestions,
    )


# ============================================================================
# Similarity / Plagiarism flag — DB-based (no LLM needed)
# ============================================================================

class SimilarityHit(BaseModel):
    paper: Paper
    score: float        # 0.0..1.0 (ts_rank normalized)
    matched_terms: list[str]


class SimilarityResponse(BaseModel):
    input_excerpt: str
    word_count: int
    risk_level: str     # 'low' | 'medium' | 'high'
    top_score: float
    hits: list[SimilarityHit]


@app.post("/api/v1/similarity/check", response_model=SimilarityResponse)
def similarity_check(payload: dict) -> SimilarityResponse:
    """Cek mirip tidaknya teks user vs koleksi paper di DB.

    Pendekatan: ekstrak n-gram & kata signifikan → ts_rank Postgres FTS →
    top-5 paper paling mirip. Score 0..1 dinormalisasi dari ts_rank.
    Risk: low (<0.05), medium (<0.15), high (>=0.15).
    """
    text = (payload.get("text") or "").strip()
    limit = int(payload.get("limit") or 5)
    if not text or len(text) < 50:
        raise HTTPException(status_code=400, detail="text minimal 50 karakter")
    if limit < 1 or limit > 20:
        limit = 5

    keywords = _extract_keywords(text, k=12)
    import re as _re
    safe = [_re.sub(r"[^A-Za-z0-9À-ÿ]", "", k) for k in keywords if len(k) >= 4]
    safe = [k for k in safe if k]
    if not safe:
        return SimilarityResponse(
            input_excerpt=text[:200], word_count=len(text.split()),
            risk_level="low", top_score=0.0, hits=[],
        )
    ts_query = " | ".join(safe)

    sql = """
        SELECT id, source, source_id, title, authors, journal, year, url,
               abstract, keywords, is_open_access, has_dataset, dataset_urls,
               to_char(scraped_at, 'YYYY-MM-DD"T"HH24:MI:SSOF') AS scraped_at,
               ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                       to_tsquery('simple', %(q)s)) AS rank
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', %(q)s)
         ORDER BY rank DESC
         LIMIT %(limit)s
    """
    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, {"q": ts_query, "limit": limit})
                rows = cur.fetchall()
    except Exception as exc:  # noqa: BLE001
        raise HTTPException(status_code=503, detail=f"db error: {exc}")

    hits: list[SimilarityHit] = []
    top_score = 0.0
    for r in rows:
        score = float(r["rank"]) if r["rank"] is not None else 0.0
        # Bound to 0..1 (ts_rank usually 0..1 already for simple queries).
        score = max(0.0, min(1.0, score))
        if score > top_score:
            top_score = score
        # Find which user keywords appear in title/abstract.
        haystack = ((r["title"] or "") + " " + (r["abstract"] or "")).lower()
        matched = [k for k in keywords if k in haystack]
        paper_dict = {k: r[k] for k in Paper.model_fields if k in r}
        hits.append(SimilarityHit(paper=Paper(**paper_dict), score=score, matched_terms=matched[:8]))

    if top_score >= 0.15:
        risk = "high"
    elif top_score >= 0.05:
        risk = "medium"
    else:
        risk = "low"

    return SimilarityResponse(
        input_excerpt=text[:200],
        word_count=len(text.split()),
        risk_level=risk,
        top_score=round(top_score, 4),
        hits=hits,
    )


# ============================================================================
# Title Generator — LLM (Ollama) + paper context dari DB
# ============================================================================

class TitleSuggestion(BaseModel):
    title: str
    rationale: str
    methodology_hint: str | None = None


class TitleGenResponse(BaseModel):
    topic: str
    keywords_used: list[str]
    context_count: int
    suggestions: list[TitleSuggestion]


def _fetch_context_papers(topic: str, k: int = 8) -> list[dict]:
    """Ambil k paper teratas yang relevan dengan topik dari DB.

    Degradasi anggun: bila DB korpus crawler belum di-setup / tidak bisa
    diakses, kembalikan list kosong supaya generator judul tetap jalan
    (mode tanpa konteks, mengandalkan LLM saja) alih-alih 500.
    """
    keywords = _extract_keywords(topic, k=6)
    import re as _re
    safe = [_re.sub(r"[^A-Za-z0-9À-ÿ]", "", w) for w in keywords if len(w) >= 4]
    safe = [w for w in safe if w]
    if not safe:
        return []
    ts_query = " | ".join(safe)
    sql = """
        SELECT title, year, authors,
               ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                       to_tsquery('simple', %(q)s)) AS rank
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', %(q)s)
         ORDER BY rank DESC, year DESC NULLS LAST
         LIMIT %(k)s
    """
    try:
        with crawler_db.connect() as conn:
            with conn.cursor() as cur:
                cur.execute(sql, {"q": ts_query, "k": k})
                return cur.fetchall()
    except Exception as exc:  # noqa: BLE001 — korpus opsional, jangan matikan endpoint
        logger.warning("title-suggest: lewati konteks paper (DB tak tersedia): %s", exc)
        return []


def _ollama_generate(prompt: str, *, model: str = OLLAMA_MODEL,
                     temperature: float = 0.7, max_tokens: int = 600) -> str:
    """Panggil Ollama /api/generate. Return text response."""
    try:
        with httpx.Client(timeout=120.0) as client:
            r = client.post(
                f"{OLLAMA_URL}/api/generate",
                json={
                    "model": model,
                    "prompt": prompt,
                    "stream": False,
                    "options": {
                        "temperature": temperature,
                        "num_predict": max_tokens,
                    },
                },
            )
            r.raise_for_status()
            return r.json().get("response", "")
    except httpx.HTTPError as exc:
        raise HTTPException(status_code=503, detail=f"ollama error: {exc}")


@app.post("/api/v1/research/title-suggest", response_model=TitleGenResponse)
def title_suggest(payload: dict) -> TitleGenResponse:
    """Generate 5 calon judul skripsi berbasis topik + konteks paper dari DB."""
    topic = (payload.get("topic") or "").strip()
    program = (payload.get("program") or "").strip()  # contoh: 'PAI', 'Teknik Informatika'
    n = int(payload.get("n") or 5)
    if not topic or len(topic) < 5:
        raise HTTPException(status_code=400, detail="topic minimal 5 karakter")
    if n < 1 or n > 10:
        n = 5

    context_papers = _fetch_context_papers(topic, k=8)
    keywords = _extract_keywords(topic, k=6)

    if context_papers:
        ctx_lines = []
        for p in context_papers:
            year = p.get("year") or "t.t."
            title = (p.get("title") or "").strip()
            ctx_lines.append(f"- ({year}) {title}")
        context_block = "Berikut paper-paper yang sudah ada di topik serupa:\n" + "\n".join(ctx_lines)
        gap_instr = (
            "Buatkan judul yang BERBEDA dari yang sudah ada di atas — cari celah penelitian (gap), "
            "metode baru, atau sudut pandang yang belum dibahas."
        )
    else:
        context_block = "Belum ada paper serupa di koleksi internal."
        gap_instr = "Buat judul yang spesifik dan dapat dieksekusi."

    prog_line = f"Program studi: {program}\n" if program else ""

    prompt = f"""Anda adalah dosen pembimbing skripsi berpengalaman. Bantu mahasiswa merumuskan calon judul skripsi yang spesifik, fokus, dan layak dikerjakan dalam 1 semester.

Topik mahasiswa: {topic}
{prog_line}{context_block}

{gap_instr}

ANATOMI judul skripsi yang baik (ikuti polanya):
[metode/pendekatan] + [variabel/objek utama] + [kata relasi: pengaruh/hubungan/terhadap] + [variabel terikat] + [konteks objek/populasi/lokasi: "pada ... di ..."]

ATURAN MUTU (wajib dipatuhi tiap judul):
- panjang 8-20 kata, spesifik & terukur (hindari judul terlalu umum)
- gunakan Huruf Kapital di Awal Tiap Kata (Title Case)
- ada kata relasi penelitian (pengaruh/hubungan/analisis/implementasi/perbandingan)
- ada konteks objek/lokasi penelitian (mis. "pada Siswa SMA", "di Kota Makassar")
- hindari kata kabur: beberapa, suatu, berbagai, tentang, mengenai
- jangan menyalin judul paper yang sudah ada — cari sudut/gap baru

CONTOH judul kuat (tiru POLA & MUTUnya, bukan isinya):
"Pengaruh Model Pembelajaran Problem Based Learning terhadap Kemampuan Berpikir Kritis Siswa pada Mata Pelajaran IPA di SMP Negeri 1 Makassar"

Hasilkan TEPAT {n} calon judul. Untuk tiap judul beri: alasan singkat (gap/keunggulan) dan saran metode/dataset/teknik analisis.

WAJIB output JSON valid berikut, tanpa teks tambahan apa pun:
{{
  "suggestions": [
    {{"title": "judul lengkap", "rationale": "alasan singkat 1 kalimat", "methodology_hint": "saran metode/dataset/analisis 1 kalimat"}}
  ]
}}
"""
    raw = _ollama_generate(prompt, temperature=0.7, max_tokens=1100)

    # Parse JSON: model bisa wrap dalam ```json ... ```
    import re as _re
    match = _re.search(r"\{.*\}", raw, _re.DOTALL)
    suggestions: list[TitleSuggestion] = []
    if match:
        try:
            data = json.loads(match.group(0))
            for s in (data.get("suggestions") or [])[:n]:
                title = str(s.get("title", "")).strip()
                rationale = str(s.get("rationale", "")).strip()
                method = str(s.get("methodology_hint", "")).strip()
                if title:
                    suggestions.append(TitleSuggestion(
                        title=title,
                        rationale=rationale or "—",
                        methodology_hint=method or None,
                    ))
        except json.JSONDecodeError:
            pass

    # Fallback parser: line-based
    if not suggestions:
        for line in raw.splitlines():
            line = line.strip(" -*•1234567890.")
            if 15 <= len(line) <= 250:
                suggestions.append(TitleSuggestion(title=line, rationale="—"))
            if len(suggestions) >= n:
                break

    return TitleGenResponse(
        topic=topic,
        keywords_used=keywords,
        context_count=len(context_papers),
        suggestions=suggestions,
    )
