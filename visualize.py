from matplotlib import pyplot as plt
import pandas as pd
from pathlib import Path
import sys

input_filename = Path(sys.argv[1])
df = pd.read_csv(input_filename)
df[df.columns[0]] = pd.to_datetime(df[df.columns[0]], unit='s')
w, h = plt.figaspect(1/3)
plt.figure(figsize=(w, h))
plt.xlabel('Date/Time')
plt.ylabel('Concurrent GH action jobs')
plt.plot(df[df.columns[0]], df[df.columns[1]])
plt.savefig(str(input_filename.stem) + ".png")
