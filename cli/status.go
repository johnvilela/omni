package main

import (
	"errors"
	"fmt"
	"net/url"
)

type ServerStatus struct {
	App     string `json:"app"`
	Version string `json:"version"`
}

func (c *Client) Status() (ServerStatus, error) {
	var st ServerStatus
	err := c.do("GET", "/status", nil, &st)
	return st, err
}

func renderStatus(st ServerStatus, chs []Channel, llms []LLM) string {
	s := "\n" + titleStyle.Render("  SERVER") + "\n\n" +
		"  " + okStyle.Render("●") + " server — up " + dimStyle.Render(st.Version) + "\n\n" +
		titleStyle.Render("  CHANNELS") + "\n\n"
	for _, ch := range chs {
		s += "  " + renderChannel(ch) + "\n"
	}
	s += "\n" + titleStyle.Render("  LLM") + "\n\n"
	for _, l := range llms {
		s += "  " + renderLLM(l) + "\n"
	}
	s += "\n" + titleStyle.Render("  ALERTS") + "\n\n"
	if st.Version != version {
		s += "  " + warnStyle.Render("!") + " version mismatch: cli " + version +
			", server " + st.Version + " — rebuild the older one\n\n"
	} else {
		s += dimStyle.Render("  no alerts") + "\n\n"
	}
	return s
}

func runStatus(c *Client) int {
	st, err := c.Status()
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			fmt.Println("\n  " + errStyle.Render("●") + " server — down")
			fmt.Println(helpStyle.Render("    start it with: " + app + "-server"))
			return 1
		}
		return fail(err)
	}
	chs, err := c.Channels()
	if err != nil {
		return fail(err)
	}
	llms, err := c.LLMs()
	if err != nil {
		return fail(err)
	}
	fmt.Print(renderStatus(st, chs, llms))
	return 0
}
