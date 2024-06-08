# audiobook-repack

This CLI util does following:

1. recursively searches files using glob patterns: *.mp3, *.m4b, etc.
2. flattens file structure: ./chapte01/001.mp3 -> chapter01_001.mp3
3. sorts files using human ordering: 010.mp3 > 2.mp3
4. appends files into zip archive with STORE compression


## Usage
```
audiobook-repack <flags> DIR1 DIR2 DIR3 ...

-cpu-profile value
  	enable pprof for CPU and write to specified file
-g value
    	file globs to append int output archive. Default values: *.mp3
-o string
    	output zip file
-sauce
    	print source code
```