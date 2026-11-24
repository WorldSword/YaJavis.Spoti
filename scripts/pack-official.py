"""Write an architecture-specific official package from its executable."""
import json
import pathlib
import sys
import zipfile

out, arch, plugin_id, binary = sys.argv[1:]
root = pathlib.Path("plugins")
metadata_dir = "vpn-rust" if plugin_id == "vpn" else plugin_id
manifest = json.loads((root / metadata_dir / "plugin.json").read_text(encoding="utf-8"))
if manifest["id"] != plugin_id or manifest["kind"] != "rust" or manifest["provides"] != [plugin_id]:
    raise SystemExit(f"Unexpected manifest for {plugin_id}")
catalog = json.loads((root / "index.json").read_text(encoding="utf-8"))["plugins"]
item = next(entry for entry in catalog if entry["id"] == plugin_id)
for field in ("description", "name", "category", "version"):
    value = manifest[field]
    if not value or "???" in value or "\ufffd" in value or value != item[field]:
        raise SystemExit(f"Invalid or inconsistent {field} for {plugin_id}")
manifest["arch"] = arch
with zipfile.ZipFile(pathlib.Path(out, f"plugin-{plugin_id}-{arch}.zip"), "w", zipfile.ZIP_DEFLATED) as package:
    package.writestr("plugin.json", json.dumps(manifest, ensure_ascii=False))
    package.write(binary, manifest["main"])
