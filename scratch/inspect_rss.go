package main

import (
	"fmt"
	"log"
	"github.com/mmcdole/gofeed"
)

func main() {
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL("https://news.ycombinator.com/rss")
	if err != nil {
		log.Fatalf("error parsing feed: %v", err)
	}

	for i, item := range feed.Items {
		if i > 5 {
			break
		}
		fmt.Printf("Item %d:\n", i)
		fmt.Printf("  Title: %s\n", item.Title)
		fmt.Printf("  GUID:  %s\n", item.GUID)
		fmt.Printf("  Link:  %s\n", item.Link)
		fmt.Println()
	}
}
