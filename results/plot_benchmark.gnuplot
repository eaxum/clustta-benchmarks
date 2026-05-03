# Clustta Benchmark Visualization
# Auto-generated - run with: gnuplot plot_benchmark.gnuplot
#
# Mirrors the style used in the Blender Studio VCS benchmark:
# https://studio.blender.org/blog/svn-vs-git-lfs/

# Dark theme colours
set title textcolor rgb "#f0f0f0"
set ylabel textcolor rgb "#f0f0f0"
set y2label textcolor rgb "#f0f0f0"
set xlabel textcolor rgb "#f0f0f0"
set key textcolor rgb "#f0f0f0"
set xtics textcolor rgb "#f0f0f0"
set ytics textcolor rgb "#f0f0f0"
set border lc rgb "#f0f0f0"

set linetype 1 lc rgb "#ffa500"
set linetype 2 lc rgb "#44cc44"
set linetype 3 lc rgb "#c0c0c0"
set linetype 4 lc rgb "#0087ff"
set linetype 5 lc rgb "#b050e8"
set linetype 6 lc rgb "#fc1a70"
set term pngcairo size 1200,960 background "#2b2b2b" font "Sans,10"
set output 'benchmark_per_system.png'
set multiplot layout 3, 2 columns

set ylabel 'Size (GB)'
set y2label 'Time (Seconds)'
set xlabel 'Number of commits'
set grid
set datafile separator ','
set key top left
set style fill solid 0.35
set boxwidth 0.6 relative

set title 'Clustta'
plot './test_clustta.csv' using 1:($2/1e3) with lines title 'Local checkout (GB)', \
     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \
     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \
     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \
     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'

set title 'Git LFS'
plot './test_git_lfs.csv' using 1:($2/1e3) with lines title 'Local checkout (GB)', \
     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \
     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \
     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \
     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'

set title 'Git'
plot './test_git.csv' using 1:($2/1e3) with lines title 'Local checkout (GB)', \
     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \
     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \
     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \
     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'

set title 'Perforce'
plot './test_perforce.csv' using 1:($2/1e3) with lines title 'Local checkout (GB)', \
     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \
     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \
     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \
     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'

set title 'SVN'
plot './test_svn.csv' using 1:($2/1e3) with lines title 'Local checkout (GB)', \
     '' using 1:($3/1e3) with lines title 'Metadata (GB)', \
     '' using 1:($7/1e3) with lines title 'Sum added files (GB)', \
     '' using 1:($5/1e2) with boxes title 'Modified / commit (×0.01 GB)', \
     '' using 1:($8/1e2) with lines title 'Sum commit time (×0.01 s)'

unset multiplot

# ── Summary comparison ──
set term pngcairo size 1200,500 background "#2b2b2b" font "Sans,10"
set output 'benchmark_summary.png'
set multiplot layout 1, 2

set title 'Cumulative commit time'
set ylabel 'Time (s)'
set xlabel 'Number of commits'
unset y2label
plot './test_clustta.csv' using 1:8 with lines lw 2 lc rgb '#b050e8' title 'Clustta', \
     './test_git_lfs.csv' using 1:8 with lines lw 2 lc rgb '#44cc44' title 'Git LFS', \
     './test_git.csv' using 1:8 with lines lw 2 lc rgb '#ffa500' title 'Git', \
     './test_perforce.csv' using 1:8 with lines lw 2 lc rgb '#0087ff' title 'Perforce', \
     './test_svn.csv' using 1:8 with lines lw 2 lc rgb '#c0c0c0' title 'SVN'

set title 'Metadata / repository size'
set ylabel 'Size (GB)'
plot './test_clustta.csv' using 1:($3/1e3) with lines lw 2 lc rgb '#b050e8' title 'Clustta', \
     './test_git_lfs.csv' using 1:($3/1e3) with lines lw 2 lc rgb '#44cc44' title 'Git LFS', \
     './test_git.csv' using 1:($3/1e3) with lines lw 2 lc rgb '#ffa500' title 'Git', \
     './test_perforce.csv' using 1:($3/1e3) with lines lw 2 lc rgb '#0087ff' title 'Perforce', \
     './test_svn.csv' using 1:($3/1e3) with lines lw 2 lc rgb '#c0c0c0' title 'SVN'

unset multiplot
