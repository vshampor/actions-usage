Check the readme for the original repo first.

The actions-usage will now dump a `concurrent_jobs_*repository_name*.csv` file when invoked, with columns of UNIX timestamps vs. count of concurrent jobs at that moment. You can then call `visualize.py` to get a PNG step graph of that data:

```
python visualize.py concurrent_jobs_nncf.csv
```
