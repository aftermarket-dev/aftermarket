# Builds parks.db, the small database both Datasette versions serve.
import sqlite3, sys
path = sys.argv[1] if len(sys.argv) > 1 else "parks.db"
db = sqlite3.connect(path)
db.executescript("""
DROP TABLE IF EXISTS parks;
CREATE TABLE parks (id INTEGER PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL,
                    established INTEGER, area_km2 REAL, visitors_2023 INTEGER);
""")
rows = [
    (1, "Yellowstone", "WY", 1872, 8983.2, 4501382),
    (2, "Yosemite", "CA", 1890, 3082.7, 3897070),
    (3, "Grand Canyon", "AZ", 1919, 4862.9, 4733705),
    (4, "Zion", "UT", 1919, 595.9, 4623238),
    (5, "Acadia", "ME", 1916, 198.6, 3879890),
    (6, "Olympic", "WA", 1938, 3733.8, 2943986),
    (7, "Glacier", "MT", 1910, 4100.0, 3208755),
    (8, "Joshua Tree", "CA", 1994, 3217.9, 2954211),
    (9, "Sequoia", "CA", 1890, 1635.2, 1220950),
    (10, "Arches", "UT", 1971, 310.3, 1482045),
    (11, "Everglades", "FL", 1934, 6106.5, 1118300),
    (12, "Denali", "AK", 1917, 19185.8, 498722),
]
db.executemany("INSERT INTO parks VALUES (?,?,?,?,?,?)", rows)
db.commit()
