import hashlib, json, os, zipfile

VER = "1.6.0"
BASE = r"d:\Code\Quant Harness\AIQuant\build\bin"
DIST = r"d:\Code\Quant Harness\AIQuant\dist"
ZIP = os.path.join(DIST, f"QuantBot-{VER}.zip")

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

# --- files to include in the ZIP (runnable layout), runtime/temp excluded ---
zip_entries = []
def add(rel):
    full = os.path.join(BASE, rel)
    if not os.path.exists(full):
        raise FileNotFoundError(f"缺少文件: {full}")
    zip_entries.append((rel, full))

# --- manifest (updater-tracked set: code + small assets, excludes DBs) ---
manifest_files = [
    "data/stock_dict.json",
]
for f in sorted(os.listdir(os.path.join(BASE, "models"))):
    manifest_files.append("models/" + f)
manifest_files += [
    "用户手册.md",
    "QuantBot.exe",
    "agent.dll",  # 决策脑二进制：随 exe 分发，缺失时宿主回退 harness
]

manifest = {
    "version": VER,
    "min_version": "1.0.0",
    "files": [
        {"path": p, "size": None, "sha256": sha256(os.path.join(BASE, p))}
        for p in manifest_files
    ],
}
manifest_path = os.path.join(BASE, "manifest.json")
with open(manifest_path, "w", encoding="utf-8") as mf:
    mf.write(json.dumps(manifest, ensure_ascii=False, separators=(",", ":")))

# --- assemble zip file list (manifest now exists under BASE) ---
# root files
add("QuantBot.exe")
add("agent.dll")
add("用户手册.md")
add("manifest.json")

# config / data / database dirs (whitelist, skip runtime & temp)
def add_dir(subdir, exclude_pred=None):
    base_dir = os.path.join(BASE, subdir)
    for root, _dirs, files in os.walk(base_dir):
        for f in files:
            full = os.path.join(root, f)
            rel = os.path.relpath(full, BASE).replace("\\", "/")
            # skip runtime-only subtrees
            if any(rel.startswith(x) for x in ["data/trades/", "data/market/", "log/", "reports/"]):
                continue
            if exclude_pred and exclude_pred(rel):
                continue
            zip_entries.append((rel, full))

add_dir("config")
add_dir("data", exclude_pred=lambda r: r in ("data/stock.duckdb.new",))
add_dir("database")
add_dir("models")

# --- write zip ---
os.makedirs(DIST, exist_ok=True)
if os.path.exists(ZIP):
    os.remove(ZIP)
total = 0
with zipfile.ZipFile(ZIP, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    for rel, full in zip_entries:
        z.write(full, rel)
        total += 1
    print(f"[zip] {total} entries -> {ZIP}")

# --- sha256 of the zip ---
zip_sha = sha256(ZIP)
sha_path = ZIP + ".sha256"
with open(sha_path, "w", encoding="utf-8") as f:
    f.write(zip_sha)
    f.write("\n")
print(f"[sha] {zip_sha}")
print(f"[size] {os.path.getsize(ZIP)/1024/1024:.2f} MB")
print(f"[ok] {ZIP}")
print(f"[ok] {sha_path}")