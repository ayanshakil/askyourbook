"""Generate testdata/sample.pdf: a small multi-page PDF with distinct facts per page.

Uses only the standard library and the built-in Helvetica font, so the text is
extractable by any PDF reader. Run: python3 testdata/gen_sample_pdf.py
"""

import os
import textwrap

PAGES = [
    ("The Lighthouses of Merrow Bay",
     "This guide describes the five lighthouses that protect Merrow Bay, a fictional "
     "harbour on a rocky northern coast. Each chapter covers one lighthouse: its "
     "history, its keepers, and the light it shows at night. The bay was charted in "
     "1791 by Captain Elsbeth Varga, who named it after the merrow, a sea spirit from "
     "local folklore. Shipwrecks on the Hollin Reef were common until the first light "
     "was lit. Today all five lights are automated, but the keepers' cottages remain "
     "open to visitors from April to October. The chapters that follow are arranged "
     "from north to south, in the order a ship entering the bay would pass them."),
    ("Chapter 1: Hollin Point Light",
     "Hollin Point Light was the first lighthouse in the bay, completed in 1823. It "
     "stands 31 metres tall and is built from grey granite quarried at Tarrow Hill. "
     "Its light flashes white twice every ten seconds and is visible for 18 nautical "
     "miles. The first keeper was Tobias Wren, who kept the light for 42 years and "
     "recorded every storm in a leather logbook that is now displayed in the bay "
     "museum. During the great gale of 1852 Wren rowed out alone and rescued eleven "
     "sailors from the brig Seraphine after it struck the Hollin Reef."),
    ("Chapter 2: Cassel Rock Light",
     "Cassel Rock Light sits on a bare island two kilometres offshore. It was built "
     "in 1867 after the steamer Lady Ondine sank on the rock with the loss of 63 "
     "lives. The tower is painted with red and white horizontal bands so that it can "
     "be recognised in daylight. Its light shows a steady red beam. Because the rock "
     "is often cut off by heavy seas, keepers were supplied by a basket hauled along "
     "a cable from the mainland. The longest recorded isolation lasted 47 days in "
     "the winter of 1903, when the keepers survived on salted fish and rainwater."),
    ("Chapter 3: Marrowind Lantern",
     "The Marrowind Lantern is the smallest light in the bay, only 9 metres tall. It "
     "was built in 1889 from cast iron prefabricated in a foundry at Glasmere and "
     "shipped in 214 numbered pieces. It marks the narrow channel into the inner "
     "harbour and shows a green light that flashes once every three seconds. The "
     "lantern was the first in the region to be converted to electricity, in 1921. "
     "Its keeper, Ada Fenwick, was the first woman appointed as a principal keeper "
     "on this coast, and she served from 1921 until her retirement in 1950."),
    ("Chapter 4: Southreach Light",
     "Southreach Light guards the southern entrance to the bay. Finished in 1902, it "
     "is the tallest of the five at 44 metres, and its octagonal tower is made of "
     "brick covered in white render. The light uses a rotating first-order Fresnel "
     "lens that weighs nearly six tonnes and floats on a bath of mercury so that it "
     "turns smoothly. It shows a white flash every fifteen seconds. Southreach also "
     "had a fog signal: a compressed-air horn nicknamed the Old Bull, which sounded "
     "two long blasts every minute in poor visibility until it was retired in 1987."),
    ("Chapter 5: Automation and the Bay Today",
     "All five lighthouses were automated between 1979 and 1994. Hollin Point was the "
     "last to lose its keepers, on 30 June 1994. The lights are now monitored "
     "remotely from a control room in the town of Saltmere, and solar panels power "
     "the Marrowind Lantern and Cassel Rock Light. The Merrow Bay Lighthouse Trust "
     "maintains the keepers' cottages and runs guided walks along the 23-kilometre "
     "coastal path that links the four mainland lights. Tickets for the boat trip to "
     "Cassel Rock must be booked in advance because landings depend on the tide."),
]

PAGE_W, PAGE_H = 612, 792  # US Letter
LEFT, TOP, LEADING = 72, 720, 16


def esc(s):
    return s.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")


def page_stream(title, body, number):
    # T* (next line) rather than Td, as most PDF generators do: the Go PDF
    # loader only emits a line break on T*, so Td-only text runs together.
    lines = ["BT", f"{LEADING} TL", f"/F1 18 Tf {LEFT} {TOP} Td ({esc(title)}) Tj T* T*", "/F1 11 Tf"]
    for line in textwrap.wrap(body, 85):
        lines.append(f"({esc(line)}) Tj T*")
    lines.append("ET")
    lines.append(f"BT /F1 9 Tf {PAGE_W // 2 - 10} 40 Td (Page {number}) Tj ET")
    return "\n".join(lines).encode("latin-1")


def build():
    objs = []  # 1-based object bodies

    def add(body):
        objs.append(body)
        return len(objs)

    catalog = add(None)
    pages = add(None)
    font = add(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
    kids = []
    for i, (title, body) in enumerate(PAGES, start=1):
        data = page_stream(title, body, i)
        content = add(b"<< /Length %d >>\nstream\n" % len(data) + data + b"\nendstream")
        kids.append(add(
            f"<< /Type /Page /Parent {pages} 0 R /MediaBox [0 0 {PAGE_W} {PAGE_H}] "
            f"/Resources << /Font << /F1 {font} 0 R >> >> /Contents {content} 0 R >>".encode()))
    objs[catalog - 1] = f"<< /Type /Catalog /Pages {pages} 0 R >>".encode()
    objs[pages - 1] = (f"<< /Type /Pages /Kids [{' '.join(f'{k} 0 R' for k in kids)}] "
                       f"/Count {len(kids)} >>").encode()

    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for n, body in enumerate(objs, start=1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % n + body + b"\nendobj\n"
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
    out += b"".join(b"%010d 00000 n \n" % o for o in offsets)
    out += b"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (
        len(objs) + 1, catalog, xref)
    return bytes(out)


if __name__ == "__main__":
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "sample.pdf")
    with open(path, "wb") as f:
        f.write(build())
    print(f"wrote {path} ({len(PAGES)} pages)")
