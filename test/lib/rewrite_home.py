"""Rewrite home-directory prefixes inside JSON / JSONL files.

Only string values equal to OLD or starting with OLD + "/" change, so paths
that merely contain the old home as a substring (or free text) stay put.

usage: python3 -I rewrite_home.py OLD NEW SRC DST
Prints the number of values rewritten.
"""

import json
import sys


def rewrite(value, old, new, counter):
    if isinstance(value, str):
        if value == old or value.startswith(old + "/"):
            counter[0] += 1
            return new + value[len(old):]
        return value
    if isinstance(value, list):
        return [rewrite(v, old, new, counter) for v in value]
    if isinstance(value, dict):
        return {k: rewrite(v, old, new, counter) for k, v in value.items()}
    return value


def main():
    old, new, src, dst = sys.argv[1:5]
    counter = [0]
    with open(src) as f:
        text = f.read()
    if src.endswith(".jsonl"):
        out = []
        for n, line in enumerate(text.splitlines(), 1):
            if not line.strip():
                continue
            try:
                out.append(json.dumps(rewrite(json.loads(line), old, new, counter), separators=(",", ":")))
            except json.JSONDecodeError as e:
                sys.exit(f"{src}:{n}: not JSON ({e})")
        result = "\n".join(out) + "\n"
    else:
        result = json.dumps(rewrite(json.loads(text), old, new, counter))
    with open(dst, "w") as f:
        f.write(result)
    print(counter[0])


if __name__ == "__main__":
    main()
