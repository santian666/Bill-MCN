import json
import os
import re
import socket
import subprocess
import sys
import urllib.error
import urllib.request
import zipfile


def read_app_version(backend_path):
    with open(backend_path, "r", encoding="utf-8") as f:
        text = f.read()
    m = re.search(r'const\s+appVersion\s*=\s*"([^"]+)"', text)
    if not m:
        raise ValueError(f"Could not find appVersion in {backend_path}")
    return m.group(1).strip()


def get_github_token():
    p = subprocess.Popen(
        ["git", "credential", "fill"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
    )
    out, _ = p.communicate("protocol=https\nhost=github.com\n\n")
    for line in out.splitlines():
        if line.startswith("password="):
            return line.split("=", 1)[1]
    raise RuntimeError("Failed to retrieve GitHub credential from git credential manager.")


def create_opener():
    try:
        s = socket.socket()
        s.settimeout(1)
        s.connect(("127.0.0.1", 20808))
        s.close()
        proxy = urllib.request.ProxyHandler(
            {"http": "http://127.0.0.1:20808", "https": "http://127.0.0.1:20808"}
        )
        return urllib.request.build_opener(proxy)
    except Exception:
        return urllib.request.build_opener()


def build_release_zip(gui_dir):
    exe_path = os.path.join(gui_dir, "build", "bin", "账单系统.exe")
    if not os.path.exists(exe_path):
        raise FileNotFoundError(f"Built executable not found: {exe_path}")
    dist_dir = os.path.join(gui_dir, "dist")
    os.makedirs(dist_dir, exist_ok=True)
    zip_path = os.path.join(dist_dir, "Bill-MCN-windows-amd64.zip")
    with zipfile.ZipFile(zip_path, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        zf.write(exe_path, arcname="账单系统.exe")
    return zip_path


def main():
    repo = "santian666/Bill-MCN"
    gui_dir = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
    ver = read_app_version(os.path.join(gui_dir, "backend.go"))
    tag = f"v{ver}" if not ver.startswith("v") else ver

    zip_path = build_release_zip(gui_dir)
    token = get_github_token()
    opener = create_opener()

    release_payload = {
        "tag_name": tag,
        "target_commitish": "main",
        "name": f"Bill-MCN {tag}",
        "body": f"Bill-MCN {tag} Windows 客户端发布包。",
        "draft": False,
        "prerelease": False,
    }

    print(f"Creating release {tag} for {repo}...")
    req = urllib.request.Request(
        f"https://api.github.com/repos/{repo}/releases",
        data=json.dumps(release_payload).encode("utf-8"),
        headers={
            "Authorization": f"token {token}",
            "User-Agent": "Bill-MCN-Uploader",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
        },
        method="POST",
    )

    rel = None
    try:
        with opener.open(req) as resp:
            rel = json.loads(resp.read().decode("utf-8"))
            print(f"Release created successfully with ID: {rel['id']}")
    except urllib.error.HTTPError as e:
        if e.code == 422:
            print(f"Release {tag} already exists, fetching existing release...")
            req_get = urllib.request.Request(
                f"https://api.github.com/repos/{repo}/releases/tags/{tag}",
                headers={
                    "Authorization": f"token {token}",
                    "User-Agent": "Bill-MCN-Uploader",
                    "Accept": "application/vnd.github+json",
                },
            )
            with opener.open(req_get) as resp_get:
                rel = json.loads(resp_get.read().decode("utf-8"))
                print(f"Fetched existing release ID: {rel['id']}")
        else:
            body = e.read().decode("utf-8", errors="replace")
            print(f"HTTP Error {e.code}: {body}")
            sys.exit(1)

    upload_url = rel["upload_url"].split("{")[0]
    assets = rel.get("assets", [])
    fname = os.path.basename(zip_path)

    for asset in assets:
        if asset.get("name") == fname:
            del_url = asset.get("url")
            print(f"Deleting existing asset {fname} (ID {asset.get('id')})...")
            del_req = urllib.request.Request(
                del_url,
                headers={
                    "Authorization": f"token {token}",
                    "User-Agent": "Bill-MCN-Uploader",
                    "Accept": "application/vnd.github+json",
                },
                method="DELETE",
            )
            with opener.open(del_req) as del_resp:
                del_resp.read()

    size = os.path.getsize(zip_path)
    print(f"Uploading {fname} ({size / (1024 * 1024):.2f} MB)...")
    with open(zip_path, "rb") as af:
        file_data = af.read()

    up_req = urllib.request.Request(
        f"{upload_url}?name={fname}",
        data=file_data,
        headers={
            "Authorization": f"token {token}",
            "User-Agent": "Bill-MCN-Uploader",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/zip",
            "Content-Length": str(size),
        },
        method="POST",
    )
    with opener.open(up_req) as up_resp:
        res = json.loads(up_resp.read().decode("utf-8"))
        print(f"Uploaded {fname} -> {res.get('browser_download_url')}")

    print(f"\nRelease {tag} published successfully!")


if __name__ == "__main__":
    main()
